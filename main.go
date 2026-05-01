package main

import (
    "embed"
    "encoding/json"
    "fmt"
    "io/fs"
    "net"
    "net/http"
    "os"
    "os/exec"
    "regexp"
    "runtime"
    "strconv"
    "strings"
    "sync"
    "time"
)

//go:embed web/*
var webFS embed.FS

type TargetStats struct {
    IP          string  `json:"ip"`
    IsUp        bool    `json:"isUp"`
    LastLatency int64   `json:"lastLatencyMs"`
    AvgLatency  float64 `json:"avgLatencyMs"`
    Sent        int64   `json:"sent"`
    Received    int64   `json:"received"`
    LossPercent float64 `json:"lossPercent"`
    LastChecked string  `json:"lastChecked"`
}

type monitor struct {
    mu      sync.RWMutex
    targets map[string]*TargetStats
}

func newMonitor() *monitor { return &monitor{targets: map[string]*TargetStats{}} }

func (m *monitor) addTarget(ip string) error {
    ip = strings.TrimSpace(ip)
    if !isValidIP(ip) {
        return fmt.Errorf("invalid IP address")
    }
    m.mu.Lock()
    defer m.mu.Unlock()
    if _, ok := m.targets[ip]; ok {
        return fmt.Errorf("IP already exists")
    }
    m.targets[ip] = &TargetStats{IP: ip}
    return nil
}
func (m *monitor) removeTarget(ip string) { m.mu.Lock(); delete(m.targets, ip); m.mu.Unlock() }

func (m *monitor) snapshot() []*TargetStats {
    m.mu.RLock()
    defer m.mu.RUnlock()
    out := make([]*TargetStats, 0, len(m.targets))
    for _, t := range m.targets {
        c := *t
        out = append(out, &c)
    }
    return out
}

func (m *monitor) updateTarget(ip string, up bool, latency int64) {
    m.mu.Lock()
    defer m.mu.Unlock()
    t, ok := m.targets[ip]
    if !ok {
        return
    }
    t.Sent++
    if up {
        t.IsUp = true
        t.Received++
        t.LastLatency = latency
        if t.Received == 1 {
            t.AvgLatency = float64(latency)
        } else {
            t.AvgLatency = ((t.AvgLatency * float64(t.Received-1)) + float64(latency)) / float64(t.Received)
        }
    } else {
        t.IsUp = false
    }
    t.LossPercent = (float64(t.Sent-t.Received) / float64(t.Sent)) * 100
    t.LastChecked = time.Now().Format("15:04:05")
}

func isValidIP(v string) bool { return net.ParseIP(v) != nil }

var pingTimeRegex = regexp.MustCompile(`time[=<]([0-9]+)ms`)

func pingOnce(ip string) (bool, int64) {
    var cmd *exec.Cmd
    if runtime.GOOS == "windows" {
        cmd = exec.Command("ping", "-n", "1", "-w", "900", ip)
    } else {
        cmd = exec.Command("ping", "-c", "1", "-W", "1", ip)
    }
    output, err := cmd.CombinedOutput()
    if err != nil {
        return false, 0
    }
    s := strings.ReplaceAll(string(output), " ", "")
    m := pingTimeRegex.FindStringSubmatch(s)
    if len(m) < 2 {
        return true, 0
    }
    latency, _ := strconv.ParseInt(m[1], 10, 64)
    return true, latency
}

func startMonitoring(m *monitor) {
    ticker := time.NewTicker(time.Second)
    defer ticker.Stop()
    for range ticker.C {
        for _, t := range m.snapshot() {
            ip := t.IP
            go func() { up, lat := pingOnce(ip); m.updateTarget(ip, up, lat) }()
        }
    }
}

func main() {
    m := newMonitor()
    go startMonitoring(m)

    uiFS, err := fs.Sub(webFS, "web")
    if err != nil {
        panic(err)
    }

    mux := http.NewServeMux()
    mux.Handle("/", http.FileServer(http.FS(uiFS)))
    mux.HandleFunc("/api/targets", func(w http.ResponseWriter, r *http.Request) {
        switch r.Method {
        case http.MethodGet:
            writeJSON(w, m.snapshot(), http.StatusOK)
        case http.MethodPost:
            var body struct{ IP string `json:"ip"` }
            if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
                http.Error(w, "invalid json", http.StatusBadRequest)
                return
            }
            if err := m.addTarget(body.IP); err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
            }
            writeJSON(w, map[string]string{"status": "ok"}, http.StatusCreated)
        case http.MethodDelete:
            ip := r.URL.Query().Get("ip")
            if ip == "" {
                http.Error(w, "ip is required", http.StatusBadRequest)
                return
            }
            m.removeTarget(ip)
            writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
        default:
            http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        }
    })

    fmt.Println("Open http://localhost:8080")
    if err := http.ListenAndServe(":8080", mux); err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}

func writeJSON(w http.ResponseWriter, v any, code int) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(code)
    _ = json.NewEncoder(w).Encode(v)
}
