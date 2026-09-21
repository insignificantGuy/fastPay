package main

import (
	"log"

	"github.com/insignificantGuy/fastPay/internal/bootstrap/apiserver"
)

func main() {
	if err := apiserver.Run(); err != nil {
		log.Fatal(err)
	}
}
