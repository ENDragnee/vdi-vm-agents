package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"

	"github.com/gin-gonic/gin"
)

// Config loaded from Environment Variables
var (
	AgentSecret = os.Getenv("AGENT_SECRET")     // To verify requests from Next.js
	NextJsURL   = os.Getenv("NEXTJS_URL")       // e.g., http://192.168.8.146:3000
	NextJsKey   = os.Getenv("NEXTJS_API_KEY")   // To authenticate back to Next.js
	ConfigDir   = os.Getenv("NIXOS_CONFIG_DIR") // e.g., /home/vdi/vdi_nixos_config
	Hostname, _ = os.Hostname()                 // To identify this VM
)

// Payload sent to Next.js
type LogPayload struct {
	Type       string `json:"type"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
	TargetName string `json:"targetName"`
	Details    string `json:"details,omitempty"`
}

func main() {
	if AgentSecret == "" || NextJsURL == "" || ConfigDir == "" {
		log.Fatal("Missing required environment variables")
	}

	r := gin.Default()

	// Middleware to verify requests from Next.js
	r.Use(func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		if token != "Bearer "+AgentSecret {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		c.Next()
	})

	r.POST("/api/sync", handleSync)

	log.Printf("Starting VDI Agent on port 8081 for host %s", Hostname)
	r.Run("0.0.0.0:8081")
}

func handleSync(c *gin.Context) {
	// Acknowledge the request immediately so Next.js doesn't timeout
	c.JSON(http.StatusAccepted, gin.H{"status": "Sync initiated in background"})

	// Run the heavy lifting asynchronously
	go performSync()
}

func performSync() {
	sendLog("NIX_SYNC_REQUESTED", "INFO", "Started git pull and NixOS rebuild")

	// 1. Git Pull --Rebase
	pullCmd := exec.Command("git", "pull", "--rebase")
	pullCmd.Dir = ConfigDir
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		sendLog(
			"NIX_BUILD_FAILED",
			"ERROR",
			fmt.Sprintf("Git pull failed: %s", err.Error()),
			string(pullOut),
		)
		return
	}

	// 2. NixOS Rebuild Switch
	// Note: We use sudo here. The agent must have passwordless sudo for nixos-rebuild
	rebuildCmd := exec.Command("sudo", "nixos-rebuild", "switch", "--flake", ".#vdi")
	rebuildCmd.Dir = ConfigDir
	rebuildOut, err := rebuildCmd.CombinedOutput()
	if err != nil {
		sendLog(
			"NIX_BUILD_FAILED",
			"ERROR",
			fmt.Sprintf("NixOS rebuild failed: %s", err.Error()),
			string(rebuildOut),
		)
		return
	}

	sendLog(
		"NIX_BUILD_SUCCESS",
		"INFO",
		"Successfully pulled and rebuilt NixOS",
		string(rebuildOut),
	)
}

func sendLog(logType, severity, message string, details ...string) {
	detailStr := ""
	if len(details) > 0 {
		detailStr = details[0]
	}

	payload := LogPayload{
		Type:       logType,
		Severity:   severity,
		Message:    message,
		TargetName: Hostname, // Send the hostname so Next.js knows which VM this is
		Details:    detailStr,
	}

	jsonData, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", NextJsURL+"/api/agent/log", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+NextJsKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to send log to Next.js: %v", err)
		return
	}
	defer resp.Body.Close()
}
