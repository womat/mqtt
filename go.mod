module github.com/womat/mqtt

go 1.27

// v0.1.0 and v0.2.0 are an unrelated draft from 2021 with a different API.
retract [v0.1.0, v0.2.0]

require github.com/eclipse/paho.mqtt.golang v1.5.1

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
)
