package main

import (
	"log"
	"strconv"
	"sync/atomic"
)

// logError logs errors with a standard format.
func logError(err error) {
	if err != nil {
		log.Printf("[ERROR] %v", err)
	}
}

// itoa converts an int to string.
func itoa(i int) string {
	return strconv.Itoa(i)
}

var globalConnID uint32

func nextID() uint32 {
	return atomic.AddUint32(&globalConnID, 1)
}
