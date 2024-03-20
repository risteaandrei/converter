# Use the official Golang image.
# This image includes all the tools necessary for building a Go application.
FROM golang:1.18

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

# Install ffmpeg
# Update apt repository and install ffmpeg
RUN apt-get update && apt-get install -y ffmpeg

# Clean up the apt cache to reduce image size
RUN apt-get clean && rm -rf /var/lib/apt/lists/*

# Copy the source from the current directory to the Working Directory inside the container
COPY . .

# Build the Go app
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o consumer .

# Command to run the executable
CMD ["./consumer"]
