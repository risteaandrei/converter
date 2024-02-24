package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"
	"github.com/streadway/amqp"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/gridfs"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	videoDb       *mongo.Database
	mp3Db         *mongo.Database
	fsVideos      *gridfs.Bucket
	fsMp3s        *gridfs.Bucket
	rabbitChannel *amqp.Channel
)

func connectToMongoDB(uri, dbName string) (*mongo.Database, error) {
	client, err := mongo.Connect(context.Background(), options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	return client.Database(dbName), nil
}

func setupGridFS(db *mongo.Database, bucketName string) (*gridfs.Bucket, error) {
	bucket, err := gridfs.NewBucket(db)
	if err != nil {
		return nil, err
	}
	return bucket, nil
}

func connectToRabbitMQ(uri string) (*amqp.Channel, error) {
	conn, err := amqp.Dial(uri)
	if err != nil {
		return nil, err
	}
	channel, err := conn.Channel()
	if err != nil {
		return nil, err
	}
	return channel, nil
}

func loginUser(username, password string) (string, error) {
	// Placeholder for actual authentication logic.
	if username == "admin" && password == "password" {
		return "validToken", nil
	}
	return "", fmt.Errorf("invalid credentials")
}

func uploadFileToGridFS(file io.Reader, fs *gridfs.Bucket) (primitive.ObjectID, error) {
	uploadStream, err := fs.OpenUploadStream("uploadedFile")
	if err != nil {
		return primitive.NilObjectID, err
	}
	defer uploadStream.Close()

	_, err = io.Copy(uploadStream, file)
	if err != nil {
		return primitive.NilObjectID, err
	}

	return uploadStream.FileID.(primitive.ObjectID), nil
}

func publishToRabbitMQ(channel *amqp.Channel, message []byte) error {
	return channel.Publish(
		"",      // exchange
		"queue", // routing key
		false,   // mandatory
		false,   // immediate
		amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			ContentType:  "application/json",
			Body:         message,
		},
	)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	username, password, ok := r.BasicAuth()
	if !ok {
		http.Error(w, "Basic auth required", http.StatusUnauthorized)
		return
	}

	token, err := loginUser(username, password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	w.Write([]byte(token))
}

func validateToken(token string) (bool, bool, error) {
	authServiceURL := os.Getenv("AUTH_SVC_ADDRESS")
	client := &http.Client{}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/validate", authServiceURL), nil)
	if err != nil {
		return false, false, err
	}

	req.Header.Add("Authorization", token)

	resp, err := client.Do(req)
	if err != nil {
		return false, false, err
	}
	defer resp.Body.Close()

	var result struct {
		IsAdmin bool `json:"isAdmin"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, false, err
	}

	if resp.StatusCode == 200 {
		return true, result.IsAdmin, nil
	}

	return false, false, fmt.Errorf("token validation failed with status code: %d", resp.StatusCode)
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	isValid, isAdmin, err := validateToken(token)
	if err != nil || !isValid {
		http.Error(w, "Invalid token or token validation error", http.StatusUnauthorized)
		return
	}
	if !isAdmin {
		http.Error(w, "Not authorized", http.StatusForbidden)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "File upload error", http.StatusBadRequest)
		return
	}
	defer file.Close()

	fileID, err := uploadFileToGridFS(file, fsVideos)
	if err != nil {
		http.Error(w, "Failed to upload file", http.StatusInternalServerError)
		return
	}

	msg := map[string]interface{}{
		"fileID": fileID.Hex(),
	}
	msgBytes, _ := json.Marshal(msg)

	if err := publishToRabbitMQ(rabbitChannel, msgBytes); err != nil {
		http.Error(w, "Failed to publish message", http.StatusInternalServerError)
		return
	}

	w.Write([]byte("File uploaded successfully"))
}

func downloadHandler(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	isValid, isAdmin, err := validateToken(token)
	if err != nil || !isValid {
		http.Error(w, "Invalid token or token validation error", http.StatusUnauthorized)
		return
	}
	if !isAdmin {
		http.Error(w, "Not authorized", http.StatusForbidden)
		return
	}

	fidString := r.URL.Query().Get("fid")
	if fidString == "" {
		http.Error(w, "fid is required", http.StatusBadRequest)
		return
	}

	fid, err := primitive.ObjectIDFromHex(fidString)
	if err != nil {
		http.Error(w, "Invalid fid", http.StatusBadRequest)
		return
	}

	file, err := fsMp3s.OpenDownloadStream(fid)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.mp3\"", fid.Hex()))

	if _, err := io.Copy(w, file); err != nil {
		log.Println("Error streaming file:", err)
		http.Error(w, "Error streaming file", http.StatusInternalServerError)
	}
}

func main() {
	fmt.Println("Connecting to videos DB ...")
	var err error
	videoDb, err = connectToMongoDB("mongodb://localhost:27017", "videos")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Connecting to mp3s DB ...")
	mp3Db, err = connectToMongoDB("mongodb://localhost:27017", "mp3s")
	if err != nil {
		log.Fatal(err)
	}

	fsVideos, err = setupGridFS(videoDb, "videos")
	if err != nil {
		log.Fatal(err)
	}
	fsMp3s, err = setupGridFS(mp3Db, "mp3s")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Connecting to RabbitMQ ...")
	rabbitChannel, err = connectToRabbitMQ("amqp://guest:guest@rabbitmq")
	if err != nil {
		log.Fatal("Failed to connect to rabbitmq", err)
	}

	r := mux.NewRouter()
	r.HandleFunc("/login", loginHandler).Methods("POST")
	r.HandleFunc("/upload", uploadHandler).Methods("POST")
	r.HandleFunc("/download", downloadHandler).Methods("GET")

	fmt.Println("Waiting for requests")
	log.Fatal(http.ListenAndServe(":8080", r))
}
