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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFS embed.FS

const dbPath = "pc_manager.db"

type TargetStats struct {
	IP              string   `json:"ip"`
	Department      string   `json:"department"`
	IsUp            bool     `json:"isUp"`
	LastLatency     int64    `json:"lastLatencyMs"`
	AvgLatency      float64  `json:"avgLatencyMs"`
	Sent            int64    `json:"sent"`
	Received        int64    `json:"received"`
	LossPercent     float64  `json:"lossPercent"`
	LastChecked     string   `json:"lastChecked"`
	LastOnlineAt    string   `json:"lastOnlineAt"`
	LastOfflineAt   string   `json:"lastOfflineAt"`
	StatusChangedAt string   `json:"statusChangedAt"`
	DayStatus       []string `json:"dayStatus"`
}

type monitor struct {
	mu      sync.RWMutex
	targets map[string]*TargetStats
}

func newMonitor() *monitor { return &monitor{targets: map[string]*TargetStats{}} }

func sqliteExec(query string) error {
	cmd := exec.Command("sqlite3", dbPath, query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sqlite exec error: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func sqliteQueryJSON(query string, v any) error {
	cmd := exec.Command("sqlite3", "-json", dbPath, query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sqlite query error: %s", strings.TrimSpace(string(out)))
	}
	if len(out) == 0 {
		out = []byte("[]")
	}
	return json.Unmarshal(out, v)
}

func initDB() error {
	return sqliteExec(`CREATE TABLE IF NOT EXISTS targets (ip TEXT PRIMARY KEY, department TEXT NOT NULL DEFAULT '', is_up INTEGER NOT NULL DEFAULT 0, last_latency_ms INTEGER NOT NULL DEFAULT 0, avg_latency_ms REAL NOT NULL DEFAULT 0, sent INTEGER NOT NULL DEFAULT 0, received INTEGER NOT NULL DEFAULT 0, loss_percent REAL NOT NULL DEFAULT 0, last_checked TEXT NOT NULL DEFAULT '', last_online_at TEXT NOT NULL DEFAULT '', last_offline_at TEXT NOT NULL DEFAULT '', status_changed_at TEXT NOT NULL DEFAULT ''); CREATE TABLE IF NOT EXISTS ping_events (id INTEGER PRIMARY KEY AUTOINCREMENT, ip TEXT NOT NULL, event_at TEXT NOT NULL, is_up INTEGER NOT NULL, latency_ms INTEGER NOT NULL DEFAULT 0); CREATE INDEX IF NOT EXISTS idx_ping_events_ip_time ON ping_events(ip, event_at);`)
}

func (m *monitor) loadFromDB() error {
	var rows []struct {
		IP, Department  string  `json:"ip"`
		IsUp            int     `json:"is_up"`
		LastLatency     int64   `json:"last_latency_ms"`
		AvgLatency      float64 `json:"avg_latency_ms"`
		Sent            int64   `json:"sent"`
		Received        int64   `json:"received"`
		LossPercent     float64 `json:"loss_percent"`
		LastChecked     string  `json:"last_checked"`
		LastOnlineAt    string  `json:"last_online_at"`
		LastOfflineAt   string  `json:"last_offline_at"`
		StatusChangedAt string  `json:"status_changed_at"`
	}
	if err := sqliteQueryJSON(`SELECT ip, department, is_up, last_latency_ms, avg_latency_ms, sent, received, loss_percent, last_checked, last_online_at, last_offline_at, status_changed_at FROM targets;`, &rows); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		m.targets[r.IP] = &TargetStats{IP: r.IP, Department: r.Department, IsUp: r.IsUp == 1, LastLatency: r.LastLatency, AvgLatency: r.AvgLatency, Sent: r.Sent, Received: r.Received, LossPercent: r.LossPercent, LastChecked: r.LastChecked, LastOnlineAt: r.LastOnlineAt, LastOfflineAt: r.LastOfflineAt, StatusChangedAt: r.StatusChangedAt}
	}
	return nil
}

func esc(s string) string { return strings.ReplaceAll(s, "'", "''") }
func (m *monitor) addTarget(ip, department string) error {
	ip = strings.TrimSpace(ip)
	department = strings.TrimSpace(department)
	if !isValidIP(ip) {
		return fmt.Errorf("invalid IP address")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.targets[ip]; ok {
		return fmt.Errorf("IP already exists")
	}
	if err := sqliteExec(fmt.Sprintf("INSERT INTO targets (ip, department) VALUES ('%s','%s');", esc(ip), esc(department))); err != nil {
		return err
	}
	m.targets[ip] = &TargetStats{IP: ip, Department: department}
	return nil
}
func (m *monitor) removeTarget(ip string) {
	m.mu.Lock()
	delete(m.targets, ip)
	m.mu.Unlock()
	_ = sqliteExec(fmt.Sprintf("DELETE FROM targets WHERE ip='%s'; DELETE FROM ping_events WHERE ip='%s';", esc(ip), esc(ip)))
}

func (m *monitor) dailyStatus(ip string) []string {
	result := make([]string, 24)
	for i := range result {
		result[i] = "unknown"
	}
	start := time.Now().UTC().Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)
	var rows []struct {
		EventAt string `json:"event_at"`
		IsUp    int    `json:"is_up"`
	}
	q := fmt.Sprintf("SELECT event_at, is_up FROM ping_events WHERE ip='%s' AND event_at >= '%s' AND event_at < '%s' ORDER BY event_at;", esc(ip), start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err := sqliteQueryJSON(q, &rows); err != nil {
		return result
	}
	for _, r := range rows {
		ts, err := time.Parse(time.RFC3339, r.EventAt)
		if err != nil {
			continue
		}
		h := ts.UTC().Hour()
		if r.IsUp == 1 {
			result[h] = "up"
		} else if result[h] != "up" {
			result[h] = "down"
		}
	}
	return result
}

func (m *monitor) snapshot() []*TargetStats {
	m.mu.RLock()
	out := make([]*TargetStats, 0, len(m.targets))
	for _, t := range m.targets {
		c := *t
		out = append(out, &c)
	}
	m.mu.RUnlock()
	for _, t := range out {
		t.DayStatus = m.dailyStatus(t.IP)
	}
	sort.Slice(out, func(i, j int) bool {
		di := strings.ToLower(out[i].Department)
		dj := strings.ToLower(out[j].Department)
		if di == dj {
			return out[i].IP < out[j].IP
		}
		return di < dj
	})
	return out
}

func (m *monitor) updateTarget(ip string, up bool, latency int64) {
	now := time.Now().UTC()
	stamp := now.Format("2006-01-02 15:04:05")
	m.mu.Lock()
	t, ok := m.targets[ip]
	if !ok {
		m.mu.Unlock()
		return
	}
	t.Sent++
	if up {
		if !t.IsUp {
			t.StatusChangedAt = stamp
		}
		t.IsUp = true
		t.Received++
		t.LastLatency = latency
		if t.Received == 1 {
			t.AvgLatency = float64(latency)
		} else {
			t.AvgLatency = ((t.AvgLatency * float64(t.Received-1)) + float64(latency)) / float64(t.Received)
		}
		t.LastOnlineAt = stamp
	} else {
		if t.IsUp {
			t.StatusChangedAt = stamp
		}
		t.IsUp = false
		t.LastOfflineAt = stamp
	}
	t.LossPercent = (float64(t.Sent-t.Received) / float64(t.Sent)) * 100
	t.LastChecked = stamp
	dep := t.Department
	sent := t.Sent
	rec := t.Received
	loss := t.LossPercent
	avg := t.AvgLatency
	ll := t.LastLatency
	lch := t.LastChecked
	lon := t.LastOnlineAt
	lof := t.LastOfflineAt
	sch := t.StatusChangedAt
	is := 0
	if t.IsUp {
		is = 1
	}
	m.mu.Unlock()
	_ = sqliteExec(fmt.Sprintf("UPDATE targets SET department='%s', is_up=%d, last_latency_ms=%d, avg_latency_ms=%f, sent=%d, received=%d, loss_percent=%f, last_checked='%s', last_online_at='%s', last_offline_at='%s', status_changed_at='%s' WHERE ip='%s'; INSERT INTO ping_events (ip, event_at, is_up, latency_ms) VALUES ('%s','%s',%d,%d);", esc(dep), is, ll, avg, sent, rec, loss, esc(lch), esc(lon), esc(lof), esc(sch), esc(ip), esc(ip), now.Format(time.RFC3339), is, latency))
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
	if err := initDB(); err != nil {
		panic(err)
	}
	m := newMonitor()
	if err := m.loadFromDB(); err != nil {
		panic(err)
	}
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
			var body struct {
				IP         string `json:"ip"`
				Department string `json:"department"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			if err := m.addTarget(body.IP, body.Department); err != nil {
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
