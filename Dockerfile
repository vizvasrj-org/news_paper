# Use the official Golang image as the base image
FROM golang:1.21.2

# Set the working directory inside the container
WORKDIR /app

# Copy the Go module files
COPY go.mod go.sum ./

# Download the Go module dependencies
RUN go mod download

# Copy the Go source code
COPY . .

# Build the Go program
RUN go build -o main .

# Set the command to run the executable
CMD ["./main"]
