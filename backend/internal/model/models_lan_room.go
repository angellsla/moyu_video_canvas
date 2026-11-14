package model

import "time"

// Room state is a materialized view of the signed, append-only event graph.
type LANRoom struct {
	ID        string `gorm:"primaryKey;size:64"`
	State     string `gorm:"type:text;not null"`
	UpdatedAt time.Time
}

type LANRoomEvent struct {
	ID      string `gorm:"primaryKey;size:64"`
	RoomID  string `gorm:"size:64;not null;index"`
	Payload string `gorm:"type:text;not null"`
}

type LANRoomJoin struct {
	Outgoing  bool      `json:"outgoing"`
	PeerID    string    `json:"peerId,omitempty" gorm:"size:64"`
	PeerKey   string    `json:"-" gorm:"type:text"`
	Status    string    `json:"status" gorm:"size:24"`
	ID        string    `json:"id" gorm:"primaryKey;size:64"`
	RoomID    string    `json:"roomId" gorm:"size:64;not null;index"`
	NodeID    string    `json:"nodeId" gorm:"size:64;not null"`
	PublicKey string    `json:"publicKey" gorm:"type:text;not null"`
	Nickname  string    `json:"nickname" gorm:"size:80"`
	Address   string    `json:"address" gorm:"size:45"`
	Port      int       `json:"port"`
	Proof     string    `json:"-" gorm:"type:text;not null"`
	ExpiresAt time.Time `json:"expiresAt"`
}
