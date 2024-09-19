package models

import "time"

// TrafficDirection denotes the traffic flow allowed
type TrafficDirection int

const (
	// TrafficDirectionIN applies to incoming traffic
	TrafficDirectionIN TrafficDirection = iota
	// TrafficDirectionOUT applies to outgoing traffic
	TrafficDirectionOUT
	// TrafficDirectionINOUT applies to bi-directional traffic
	TrafficDirectionINOUT
)

type Acl struct {
	Src       map[TagID]struct{} `json:"src"`
	Dst       map[TagID]struct{} `json:"dst"`
	Direction TrafficDirection   `json:"direction"`
	CreatedBy string             `json:"created_by"`
	CreatedAt time.Time          `json:"created_at"`
}
