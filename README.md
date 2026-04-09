# VM Agent: NixOS Actuator (Go/Gin)

The VM Agent is a lightweight actutor service written in Go. It resides within each NixOS instance and facilitates the GitOps-driven reconciliation process between the Control Plane and the local VM state.

## 1. Technical Specification

### 1.1 Core Execution Loop
The agent listens on `0.0.0.0:8081`. Upon receiving an authenticated `POST /api/sync` request, it initiates a background thread to:
1.  **Rebase:** Pull latest changes from the origin Git repository.
2.  **Rebuild:** Execute `nixos-rebuild switch --flake .#vdi`.
3.  **Callback:** Report logs and build status back to the Control Plane.

### 1.2 Security & Authentication
- **Incoming Auth:** Requires `Authorization: Bearer ${AGENT_SECRET}` header.
- **Outgoing Auth:** Communicates with the Next.js Control Plane using `Authorization: Bearer ${NEXTJS_API_KEY}`.

## 2. Compilation & Deployment

### 2.1 Build Requirements
To ensure compatibility with the NixOS environment, the agent must be statically linked.
```bash
CGO_ENABLED=0 GOOS=linux go build -o vdi-agent main.go
```

### 2.2 Systemd Configuration
The agent is managed as a systemd service.
- **Service Path:** `/persist/bin/vdi-agent`
- **Environment:** Loaded from `/persist/etc/vdi-agent.env`.
- **Sudo Permissions:** Requires `NOPASSWD` rules for `nixos-rebuild`.

```nix
security.sudo.extraRules = [{
  users = ["vdi"];
  commands = [{
    command = "/run/current-system/sw/bin/nixos-rebuild";
    options = ["NOPASSWD"];
  }];
}];
```

## 3. Environment Variables

| Variable | Description |
| :--- | :--- |
| `AGENT_SECRET` | Pre-Shared Key (PSK) for authenticating Control Plane requests. |
| `NEXTJS_API_KEY` | Token for authenticating callbacks to the Control Plane webhook. |
| `NIXOS_CONFIG_DIR` | Path to the local Git repository containing NixOS configurations. |
