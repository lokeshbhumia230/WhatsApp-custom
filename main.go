package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func main() {
	// 1. Set up the local database to store the session
	dbLog := waLog.Stdout("Database", "WARN", true)
	container, err := sqlstore.New(context.Background(), "sqlite3", "file:store.db?_foreign_keys=on", dbLog)
	if err != nil {
		panic(err)
	}
	
	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		panic(err)
	}

	// 2. Initialize the client
	clientLog := waLog.Stdout("Client", "INFO", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)

	// 3. Connect to WhatsApp servers
	err = client.Connect()
	if err != nil {
		panic(err)
	}

	// Wait briefly to ensure the websocket connection is fully established
	time.Sleep(2 * time.Second)

	// 4. Request the pairing code if not logged in
	if !client.IsLoggedIn() {
		// IMPORTANT: Change this to the phone number you are testing with!
		// Format: Country code + phone number (no + or 00)
		targetPhone := "919999999999" 
		
		fmt.Println("Requesting pairing code...")
		
		// FIXED: Using "Chrome (Linux)" to pass WhatsApp's strict formatting validation
		code, err := client.PairPhone(context.Background(), targetPhone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
		if err != nil {
			fmt.Println("Error:", err)
		} else {
			fmt.Printf("\n====================================\n")
			fmt.Printf(" SUCCESS! Your pairing code is: %s \n", code)
			fmt.Printf("====================================\n\n")
			fmt.Println("Open WhatsApp > Linked Devices > Link with phone number instead")
		}
	} else {
		fmt.Println("Already logged in!")
	}

	// Keep the script running so you can enter the code on your phone
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	client.Disconnect()
}
