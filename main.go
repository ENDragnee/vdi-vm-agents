package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	AgentSecret = os.Getenv("AGENT_SECRET")
	NextJsKey   = os.Getenv("NEXTJS_API_KEY")
	ConfigDir   = os.Getenv("NIXOS_CONFIG_DIR")
)

// SyncRequest is the JSON sent FROM Next.js to this agent
type SyncRequest struct {
	CallbackURL string `json:"callbackUrl" binding:"required"`
	VMID        string `json:"vmId"        binding:"required"`
}

// LogPayload is the JSON sent BACK to the Next.js Webhook
// Standardized to 'targetId' to match the Prisma Log model and your Webhook API
type LogPayload struct {
	Type       string `json:"type"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
	TargetName string `json:"targetName"`
	TargetID   string `json:"targetId"` // Standardized field name
	Details    string `json:"details,omitempty"`
}

func main() {
	if AgentSecret == "" || NextJsKey == "" || ConfigDir == "" {
		log.Fatal(
			"Missing required environment variables: AGENT_SECRET, NEXTJS_API_KEY, or NIXOS_CONFIG_DIR",
		)
	}

	r := gin.Default()

	// Middleware to verify requests
	r.Use(func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		if token != "Bearer "+AgentSecret {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		c.Next()
	})

	r.POST("/api/sync", handleSync)

	currentHostname, _ := os.Hostname()
	log.Printf("Starting VDI Agent on port 8081 for host %s", currentHostname)

	// Create a server with a timeout to prevent hanging connections
	s := &http.Server{
		Addr:           "0.0.0.0:8081",
		Handler:        r,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	s.ListenAndServe()
}

func handleSync(c *gin.Context) {
	var req SyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing required fields (callbackUrl, vmId)"})
		return
	}

	// Acknowledge the request immediately so Next.js can move on
	c.JSON(http.StatusAccepted, gin.H{"status": "Sync initiated"})

	// Run the heavy lifting in a background goroutine
	go performSync(req.CallbackURL, req.VMID)
}

func performSync(callbackURL string, vmId string) {
	sendLog(callbackURL, vmId, "NIX_SYNC_REQUESTED", "INFO", "Started NixOS rebuild")

	// 1. Git Pull --Rebase
	pullCmd := exec.Command("git", "pull", "--rebase")
	pullCmd.Dir = ConfigDir
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		sendLog(
			callbackURL,
			vmId,
			"NIX_BUILD_FAILED",
			"ERROR",
			fmt.Sprintf("Git pull failed: %v", err),
			string(pullOut),
		)
		return
	}

	// 2. NixOS Rebuild Switch
	// We use absolute paths for sudo and nixos-rebuild to ensure zero path ambiguity
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
			vmId,
			"NIX_BUILD_FAILED",
			"ERROR",
			"NixOS rebuild failed",
			string(rebuildOut),
		)
		return
	}

	sendLog(
		callbackURL,
		vmId,
		"NIX_BUILD_SUCCESS",
		"INFO",
		"Successfully pulled and rebuilt NixOS",
		string(rebuildOut),
	)
}

func sendLog(callbackURL, vmId, logType, severity, message string, details ...string) {
	detailStr := ""
	if len(details) > 0 {
		detailStr = details[0]
	}

	// Fetch hostname fresh every time a log is sent.
	currentHostname, _ := os.Hostname()

	payload := LogPayload{
		Type:       logType,
		Severity:   severity,
		Message:    message,
		TargetName: currentHostname,
		TargetID:   vmId,
		Details:    detailStr,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Error encoding JSON: %v", err)
		return
	}

	req, err := http.NewRequest("POST", callbackURL, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Error creating callback request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+NextJsKey)

	// Set a reasonable timeout for the callback
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)

	if err != nil {
		log.Printf("Failed to send webhook to %s: %v", callbackURL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("Webhook returned error status: %d", resp.StatusCode)
	}
}
