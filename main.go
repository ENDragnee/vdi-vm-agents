package main

import (
	"bytes"
	"encoding/json"
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

type SyncRequest struct {
	CallbackURL string `json:"callbackUrl" binding:"required"`
	VMID        string `json:"vmId"        binding:"required"`
}

type LogPayload struct {
	Type       string `json:"type"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
	TargetName string `json:"targetName"`
	TargetId   string `json:"targetId"` // For the Log table
	VmId       string `json:"vmId"`     // For the Status update logic
	Details    string `json:"details,omitempty"`
}

func main() {
	if AgentSecret == "" || NextJsKey == "" || ConfigDir == "" {
		log.Fatal(
			"Missing environment variables: AGENT_SECRET, NEXTJS_API_KEY, or NIXOS_CONFIG_DIR",
		)
	}

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

	host, _ := os.Hostname()
	log.Printf("Starting VDI Agent on :8081 for host %s", host)

	s := &http.Server{
		Addr:         "0.0.0.0:8081",
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	s.ListenAndServe()
}

func handleSync(c *gin.Context) {
	var req SyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing vmId or callbackUrl"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"status": "Sync initiated"})
	// Execute the sync in a background thread
	go performSync(req.CallbackURL, req.VMID)
}

func performSync(callbackURL string, vmId string) {
	// 1. Initial log: Sync Requested
	sendLog(callbackURL, vmId, "NIX_SYNC_REQUESTED", "INFO", "Started NixOS configuration sync")

	// 2. Git Pull
	log.Printf("[%s] Pulling latest changes...", vmId)
	pullCmd := exec.Command("git", "pull", "--rebase")
	pullCmd.Dir = ConfigDir
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		sendLog(callbackURL, vmId, "NIX_BUILD_FAILED", "ERROR", "Git pull failed", string(pullOut))
		return
	}

	// 3. NixOS Rebuild
	log.Printf("[%s] Running nixos-rebuild...", vmId)
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
		log.Printf("[%s] Rebuild failed!", vmId)
		sendLog(
			callbackURL,
			vmId,
			"NIX_BUILD_FAILED",
			"ERROR",
			"NixOS rebuild execution failed",
			string(rebuildOut),
		)
		return
	}

	// 4. Success Log
	log.Printf("[%s] Rebuild successful!", vmId)
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

	host, _ := os.Hostname()
	payload := LogPayload{
		Type:       logType,
		Severity:   severity,
		Message:    message,
		TargetName: host,
		TargetId:   vmId, // Sends as targetId
		VmId:       vmId, // Sends as vmId (Duplicate for safety)
		Details:    detailStr,
	}

	jsonData, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", callbackURL, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Error creating request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+NextJsKey)

	// INCREASED TIMEOUT: To prevent 'context deadline exceeded'
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)

	if err != nil {
		log.Printf("Failed to send webhook to %s: %v", callbackURL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("Webhook error status %d for VM %s", resp.StatusCode, vmId)
	}
}
