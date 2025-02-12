package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Player struct {
	ID         string
	Matched    bool
	CreatedAt  time.Time
	OpponentID chan string
	RoomID     string
}

type ServerStats struct {
	TotalPlayers   int
	WaitingPlayers int
	MatchedPlayers int
	ActiveRooms    int
}

var (
	players   = make(map[string]*Player)
	rooms     = make(map[string][]string)
	pool      []*Player
	poolMutex sync.Mutex
	roomMutex sync.Mutex
)

func main() {
	http.HandleFunc("/", dashboardHandler)
	http.HandleFunc("/join", handleJoin)
	http.HandleFunc("/status/", handleStatus)
	http.HandleFunc("/stats", statsHandler)

	go matchPlayers()
	go cleanupOldRooms()

	fmt.Println("Server running on :8080")
	http.ListenAndServe(":8080", nil)
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.New("dashboard").Parse(`
	<!DOCTYPE html>
	<html lang="en">
	<head>
		<meta charset="UTF-8">
		<meta name="viewport" content="width=device-width, initial-scale=1.0">
		<title>Server Dashboard</title>
		<script src="https://unpkg.com/htmx.org@1.9.6"></script>
		<link href="https://cdn.jsdelivr.net/npm/tailwindcss@2.2.19/dist/tailwind.min.css" rel="stylesheet">
	</head>
	<body class="bg-gray-100">
		<div class="container mx-auto px-4 py-8">
			<h1 class="text-3xl font-bold mb-8 text-gray-800">Server Dashboard</h1>
			
			<div hx-get="/stats" hx-trigger="every 1s" class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
				<!-- Stats will be updated here -->
			</div>
			
			<div class="grid grid-cols-1 md:grid-cols-2 gap-6">
				<div class="bg-white rounded-lg shadow p-6">
					<h2 class="text-xl font-semibold mb-4 text-gray-700">Jugadores en Cola ({{.WaitingPlayers}})</h2>
					<div class="space-y-2">
						{{range .WaitingPlayersList}}
						<div class="flex items-center justify-between p-3 bg-gray-50 rounded">
							<span class="font-mono text-sm">{{.ID}}</span>
							<span class="text-xs text-gray-500">{{.CreatedAt.Format "15:04:05"}}</span>
						</div>
						{{end}}
					</div>
				</div>
				
				<div class="bg-white rounded-lg shadow p-6">
					<h2 class="text-xl font-semibold mb-4 text-gray-700">Salas Activas ({{.ActiveRooms}})</h2>
					<div class="space-y-2">
						{{range $room, $players := .ActiveRoomsList}}
						<div class="p-3 bg-gray-50 rounded">
							<div class="font-medium text-gray-600 mb-2">Room: {{$room}}</div>
							<div class="flex justify-between text-sm">
								<span>{{index $players 0}}</span>
								<span class="text-gray-500">vs</span>
								<span>{{index $players 1}}</span>
							</div>
						</div>
						{{end}}
					</div>
				</div>
			</div>
		</div>
	</body>
	</html>
	`))

	// Bloqueamos los mutex en orden consistente
	poolMutex.Lock()
	roomMutex.Lock()

	// Calculamos estadísticas directamente
	stats := ServerStats{
		TotalPlayers:   len(players),
		WaitingPlayers: len(pool),
		MatchedPlayers: len(players) - len(pool),
		ActiveRooms:    len(rooms),
	}

	waitingPlayers := make([]*Player, 0)
	for _, p := range players {
		if !p.Matched {
			waitingPlayers = append(waitingPlayers, p)
		}
	}

	// Hacer copia de las rooms para evitar bloqueos
	roomsCopy := make(map[string][]string)
	for k, v := range rooms {
		roomsCopy[k] = v
	}

	// Desbloqueamos antes de renderizar
	roomMutex.Unlock()
	poolMutex.Unlock()

	data := struct {
		ServerStats
		WaitingPlayersList []*Player
		ActiveRoomsList    map[string][]string
	}{
		ServerStats:        stats,
		WaitingPlayersList: waitingPlayers,
		ActiveRoomsList:    roomsCopy,
	}

	w.Header().Set("Content-Type", "text/html")
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Error rendering template: "+err.Error(), http.StatusInternalServerError)
	}
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(getStats())
}

func getStats() ServerStats {
	poolMutex.Lock()
	roomMutex.Lock()
	defer poolMutex.Unlock()
	defer roomMutex.Unlock()

	return ServerStats{
		TotalPlayers:   len(players),
		WaitingPlayers: len(pool),
		MatchedPlayers: len(players) - len(pool),
		ActiveRooms:    len(rooms),
	}
}

// Resto del código sin cambios (handleJoin, handleStatus, matchPlayers)...

func cleanupOldRooms() {
	for {
		time.Sleep(5 * time.Minute)

		// Orden de bloqueo consistente: primero poolMutex luego roomMutex
		poolMutex.Lock()
		roomMutex.Lock()

		for room, roomPlayers := range rooms {
			_, p1Exists := players[roomPlayers[0]]
			_, p2Exists := players[roomPlayers[1]]

			if !p1Exists && !p2Exists {
				delete(rooms, room)
			}
		}

		roomMutex.Unlock()
		poolMutex.Unlock()
	}
}

// [Las funciones handleJoin, handleStatus, matchPlayers permanecen iguales]
func handleJoin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	query := r.URL.Query()
	playerID := query.Get("id")

	if playerID == "" {
		http.Error(w, "ID is required", http.StatusBadRequest)
		return
	}

	player := &Player{
		ID:         playerID,
		Matched:    false,
		CreatedAt:  time.Now(),
		OpponentID: make(chan string, 1),
		RoomID:     "",
	}

	poolMutex.Lock()
	players[playerID] = player
	pool = append(pool, player)
	poolMutex.Unlock()

	response := map[string]string{
		"status":   "waiting",
		"playerID": playerID,
	}
	json.NewEncoder(w).Encode(response)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	playerID := r.URL.Path[len("/status/"):]
	if playerID == "" {
		http.Error(w, "ID is required", http.StatusBadRequest)
		return
	}

	poolMutex.Lock()
	player, exists := players[playerID]
	poolMutex.Unlock()

	if !exists {
		http.Error(w, "Player not found", http.StatusNotFound)
		return
	}

	select {
	case opponentID := <-player.OpponentID:
		response := map[string]string{
			"status":     "matched",
			"opponentID": opponentID,
			"roomID":     player.RoomID,
		}
		json.NewEncoder(w).Encode(response)

		// Limpiar jugador después del match
		poolMutex.Lock()
		delete(players, playerID)
		poolMutex.Unlock()
	default:
		response := map[string]string{
			"status": "waiting",
		}
		json.NewEncoder(w).Encode(response)
	}
}

// Modificación en matchPlayers para registrar las salas
func matchPlayers() {
	for {
		poolMutex.Lock()
		if len(pool) >= 2 {
			p1 := pool[0]
			p2 := pool[1]

			roomID := uuid.New().String()

			p1.RoomID = roomID
			p2.RoomID = roomID
			p1.Matched = true
			p2.Matched = true

			pool = pool[2:]

			roomMutex.Lock()
			rooms[roomID] = []string{p1.ID, p2.ID}
			roomMutex.Unlock()

			p1.OpponentID <- p2.ID
			p2.OpponentID <- p1.ID
		}
		poolMutex.Unlock()
		time.Sleep(1 * time.Second)
	}
}
