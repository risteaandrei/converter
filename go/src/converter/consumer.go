package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/streadway/amqp"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/gridfs"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	// Connect to MongoDB
	mongoClient, err := mongo.Connect(context.TODO(), options.Client().ApplyURI("mongodb://mongo_admin:admin123@filesdb-service:27017"))
	if err != nil {
		log.Fatal(err)
	}
	dbVideos := mongoClient.Database("videos")
	dbMp3s := mongoClient.Database("mp3s")

	// Establish RabbitMQ connection
	conn, err := amqp.Dial("amqp://guest:guest@rabbitmq/")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	channel, err := conn.Channel()
	if err != nil {
		log.Fatal(err)
	}
	defer channel.Close()

	msgs, err := channel.Consume(
		os.Getenv("VIDEO_QUEUE"), // queue name
		"",                       // consumer
		false,                    // auto-ack
		false,                    // exclusive
		false,                    // no-local
		false,                    // no-wait
		nil,                      // args
	)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		for d := range msgs {
			err := toMP3Start(d.Body, dbVideos, dbMp3s, channel, d.DeliveryTag)
			if err != nil {
				channel.Nack(d.DeliveryTag, false, true) // requeue the message
			} else {
				channel.Ack(d.DeliveryTag, false)
			}
		}
	}()

	fmt.Println("Waiting for messages. To exit press CTRL+C")
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("Shutting down")
}

type videoIDStruct struct {
	FileID string `json:"fileID"`
}

func toMP3Start(videoID []byte, dbVideos *mongo.Database, dbMp3s *mongo.Database, channel *amqp.Channel, deliveryTag uint64) error {
	fmt.Println("Received video id", string(videoID))

	var vid videoIDStruct
	if err := json.Unmarshal(videoID, &vid); err != nil {
		fmt.Println("Error unmarshalling videoID:", err)
		return err
	}

	// Assuming videoID is the GridFS file ID for the video file
	bucket, _ := gridfs.NewBucket(dbVideos)
	var buf bytes.Buffer
	fileID, err := primitive.ObjectIDFromHex(string(vid.FileID))
	if err != nil {
		fmt.Println("Error ObjectIDFromHex:", err)
		return err
	}
	_, err = bucket.DownloadToStream(fileID, &buf)
	if err != nil {
		fmt.Println("Error DownloadToStream:", err)
		return err
	}

	// Assume buf now contains the video file. We would save this to a temporary file to process with ffmpeg.
	tmpVideoFile := "/tmp/" + string(vid.FileID) + ".video"
	err = os.WriteFile(tmpVideoFile, buf.Bytes(), 0644)
	if err != nil {
		fmt.Println("Error writing to file:", err)
		return err
	}

	// Convert video to MP3 using ffmpeg
	tmpMp3File := "/tmp" + string(vid.FileID) + ".mp3" // This should be a unique temporary file for each conversion
	cmd := exec.Command("ffmpeg", "-i", tmpVideoFile, "-vn", "-ar", "44100", "-ac", "2", "-b:a", "192k", tmpMp3File)
	err = cmd.Run()
	if err != nil {
		fmt.Println("Error converting to mp3:", err)
		return err
	}

	fmt.Println("Converted to MP3")

	// Read the MP3 file and store it in GridFS in the mp3s database
	mp3Data, err := os.ReadFile(tmpMp3File)
	if err != nil {
		return err
	}

	mp3Bucket, _ := gridfs.NewBucket(dbMp3s)
	uploadStream, err := mp3Bucket.OpenUploadStream("converted.mp3") // You might want to use a more descriptive or unique name
	if err != nil {
		return err
	}

	if _, err = uploadStream.Write(mp3Data); err != nil {
		return err
	}
	if err = uploadStream.Close(); err != nil {
		return err
	}

	mp3id := uploadStream.FileID
	fmt.Println("Written MP3 to grid, id:", mp3id)

	mp3Message := struct {
		MP3FileID string `json:"mp3FileId"`
	}{
		MP3FileID: fmt.Sprint(mp3id),
	}

	mp3MessageBytes, err := json.Marshal(mp3Message)
	if err != nil {
		return err
	}

	err = channel.Publish(
		"",                     // exchange
		os.Getenv("MP3_QUEUE"), // routing key or queue name
		false,                  // mandatory
		false,                  // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        mp3MessageBytes,
		},
	)
	if err != nil {
		return err
	}

	fmt.Println("Published to MP3 queue file id", string(mp3MessageBytes))

	// Cleanup temporary files
	os.Remove(tmpVideoFile)
	os.Remove(tmpMp3File)

	return nil
}
