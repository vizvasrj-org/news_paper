# Use a Golang image whose Go version matches (or exceeds) the "go" line in go.mod
FROM golang:1.25

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
EXPOSE 8080
CMD ["./main"]
