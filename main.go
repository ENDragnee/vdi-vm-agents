package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	// 1. Initialize the default Gin router
	r := gin.Default()

	// 2. Define a simple GET route
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

	// 3. Start the server (defaults to :8080)
	r.Run()
}
