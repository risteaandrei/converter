package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
)

var db *sql.DB

// TODO: these should be moved out of codebase into hidden location
const (
	JWT_SECRET  = "JWT_SECRET"
	PG_USER     = "postgres"
	PG_USERS_DB = "auth"
	PG_PASSWORD = "secret"
)

func main() {
	var err error
	connString := fmt.Sprintf("user=%v dbname=%v password=%v sslmode=disable", PG_USER, PG_USERS_DB, PG_PASSWORD)
	db, err = sql.Open("postgres", connString)
	if err != nil {
		log.Fatal(err)
	}

	r := mux.NewRouter()

	r.HandleFunc("/login", loginHandler).Methods("POST")
	r.HandleFunc("/validate", validateHandler).Methods("POST")

	fmt.Println("Server starting on port 8080...")
	http.ListenAndServe(":8080", r)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	var auth struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&auth); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	var email, password string
	err := db.QueryRow("SELECT email, password FROM users WHERE email = $1", auth.Username).Scan(&email, &password)
	if err != nil {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	if auth.Username != email || auth.Password != password {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	tokenString, err := createJWT(auth.Username, JWT_SECRET, true)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": tokenString})
}

func validateHandler(w http.ResponseWriter, r *http.Request) {
	tokenString := r.Header.Get("Authorization")
	tokenString = strings.TrimPrefix(tokenString, "Bearer ")

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(JWT_SECRET), nil
	})

	if err != nil {
		http.Error(w, "Not authorized", http.StatusForbidden)
		return
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(claims)
	} else {
		http.Error(w, "Not authorized", http.StatusForbidden)
	}
}

func createJWT(username, secret string, authz bool) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"username": username,
		"exp":      time.Now().Add(24 * time.Hour).Unix(),
		"iat":      time.Now().Unix(),
		"admin":    authz,
	})

	tokenString, err := token.SignedString([]byte(secret))
	return tokenString, err
}
