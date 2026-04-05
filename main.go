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

var (
	AgentSecret = os.Getenv("AGENT_SECRET")
	NextJsKey   = os.Getenv("NEXTJS_API_KEY")
	ConfigDir   = os.Getenv("NIXOS_CONFIG_DIR")
	Hostname, _ = os.Hostname()
)

// Request coming FROM Next.js
type SyncRequest struct {
	CallbackURL string `json:"callbackUrl" binding:"required"`
}

// Payload going TO Next.js
type LogPayload struct {
	Type       string `json:"type"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
	TargetName string `json:"targetName"`
	Details    string `json:"details,omitempty"`
}

func main() {
	r := gin.Default()

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
	var req SyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing callbackUrl"})
		return
	}

	// Acknowledge the request immediately
	c.JSON(http.StatusAccepted, gin.H{"status": "Sync initiated"})

	// Pass the dynamically provided URL to the background worker
	go performSync(req.CallbackURL)
}

func performSync(callbackURL string) {
	sendLog(callbackURL, "NIX_SYNC_REQUESTED", "INFO", "Started git pull and NixOS rebuild")

	// 1. Git Pull --Rebase
	pullCmd := exec.Command("git", "pull", "--rebase")
	pullCmd.Dir = ConfigDir
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		sendLog(
			callbackURL,
			"NIX_BUILD_FAILED",
			"ERROR",
			fmt.Sprintf("Git pull failed: %v", err),
			string(pullOut),
		)
		return
	}

	// 2. NixOS Rebuild Switch
	// rebuildCmd := exec.Command("sudo", "nixos-rebuild", "switch", "--flake", ".#vdi")
	rebuildCmd := exec.Command(
		"/run/wrappers/bin/sudo",
		"/run/current-system/sw/bin/nixos-rebuild",
		"switch",
		"--flake",
		".#vdi",
	)
	rebuildCmd.Dir = ConfigDir
	rebuildOut, err := rebuildCmd.CombinedOutput()
	if err != nil {
		sendLog(
			callbackURL,
			"NIX_BUILD_FAILED",
			"ERROR",
			fmt.Sprintf("NixOS rebuild failed: %v", err),
			string(rebuildOut),
		)
		return
	}

	sendLog(
		callbackURL,
		"NIX_BUILD_SUCCESS",
		"INFO",
		"Successfully pulled and rebuilt NixOS",
		string(rebuildOut),
	)
}

func sendLog(callbackURL, logType, severity, message string, details ...string) {
	detailStr := ""
	if len(details) > 0 {
		detailStr = details[0]
	}

	payload := LogPayload{
		Type:       logType,
		Severity:   severity,
		Message:    message,
		TargetName: Hostname,
		Details:    detailStr,
	}

	jsonData, _ := json.Marshal(payload)
	// Use the callbackURL provided by Next.js!
	req, _ := http.NewRequest("POST", callbackURL, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+NextJsKey)

	client := &http.Client{}
	client.Do(req) // Fire and forget
}
