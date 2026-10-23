# Deploying the UniCompute Coordinator on Render

This guide outlines how to host the UniCompute Central Coordinator on [Render](https://render.com), along with key architectural considerations and limitations of PaaS platforms.

---

## 1. Architectural Considerations on Render

| Feature | Supported on Render? | Notes & Workaround |
|---|---|---|
| **WebSocket Signaling (WSS)** | **YES (Native)** | Render terminates TLS and provides free HTTPS/WSS on port 443 out of the box. |
| **Health Checks & APIs** | **YES** | Health endpoint at `/health` and node registry at `/api/nodes`. |
| **Direct Peer-to-Peer Data** | **YES (Unaffected)** | Data travels directly between laptops over UDP hole punching; never touches Render. |
| **Embedded UDP TURN Relay** | **NO** | Render Web Services only route HTTP/TCP. Raw UDP port 3478 is not exposed. Set `UNICOMPUTE_TURN=false`. |
| **Free Tier Spin-Down** | **Notice** | Free instances sleep after 15 min of inactivity. Keep warm using UptimeRobot / cron-job.org hitting `/health`. |

---

## 2. Step-by-Step Deployment Instructions

### Method A: Connect GitHub to Render Dashboard (Easiest)

1. Push your code to GitHub:
   ```bash
   git add .
   git commit -m "Add Render support"
   git push origin main
   ```

2. Go to the [Render Dashboard](https://dashboard.render.com/) and click **New +** $\to$ **Web Service**.
3. Select your GitHub repository.
4. Fill in the service settings:
   - **Name**: `unicompute-coordinator`
   - **Language / Runtime**: `Go`
   - **Build Command**: `go build -o unicompute-coordinator ./cmd/unicompute-coordinator`
   - **Start Command**: `./unicompute-coordinator`
   - **Plan**: `Free` (or `Starter`)
5. Under **Advanced** $\to$ **Health Check Path**, enter: `/health`
6. Under **Environment Variables**, add:
   - `UNICOMPUTE_TURN`: `false`
   - `UNICOMPUTE_REALM`: `unicompute.edu`
7. Click **Create Web Service**.

---

### Method B: Render Blueprint (`render.yaml`)

Because [`render.yaml`](file:///c:/Users/HP/Desktop/Noval/Projects/UniComputeOBJ1/render.yaml) is included in the root directory:
1. Go to [Render Dashboard](https://dashboard.render.com/) $\to$ **Blueprints**.
2. Select your repository.
3. Render will automatically detect `render.yaml` and configure the Web Service with health check and environment variables.
4. Click **Apply**.

---

## 3. Connecting Volunteer Laptops to Render

Once deployed, Render gives you a URL like:
`https://unicompute-coordinator.onrender.com`

Volunteer student laptops can now connect from anywhere in the world using:

```powershell
.\bin\unicompute-node.exe -id student-laptop-1 -signaling wss://unicompute-coordinator.onrender.com/ws
```

---

## 4. NAT Traversal & Fallback Strategy with Render

Since Render cannot serve as a UDP TURN relay, how does UniCompute handle NAT traversal?

1. **Direct P2P (80–85% of connections)**:
   - The coordinator exchanges ICE candidates.
   - The laptops use Google's free public STUN (`stun.l.google.com:19302`) to discover their public UDP endpoints.
   - Dual-stack IPv4/IPv6 hole punching connects them directly.
2. **Strict Symmetric NAT / Firewall Fallback**:
   - If direct UDP hole punching fails between two firewalled laptops, use UniCompute's built-in **Contributor Relay** (`unicompute-relay` or student laptops running with `-run-relay`).
   - Alternatively, supply a free external TURN server (such as Metered.ca's 50GB free tier) in `node.NodeConfig`.
