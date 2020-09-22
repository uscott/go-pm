package main

import (
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/uscott/go-api-deribit/api"
	restarter "github.com/uscott/go-restarter"
)

type CX struct {
	*api.Subordinate
	initiated chan bool
}

func (x *CX) Done() chan bool {
	return x.ChDone
}

func (x *CX) Error() chan error {
	return x.ChError
}

func (x *CX) Initialize() {
	if x.Subordinate == nil {
		x.Subordinate = api.NewSubordinate()
		x.initiated <- true
	}
}

func (x *CX) WaitTime() time.Duration {
	return x.Wait
}

func (x *CX) Run() {
	<-x.initiated
	rand.Seed(time.Now().UnixNano())
	x.Wait = 15 * time.Second
	for {
		n := rand.Int31n(20)
		fmt.Println(n)
		switch n {
		case 0:
			x.ChError <- fmt.Errorf("n == 0")
			return
		case 19:
			x.ChDone <- true
			return
		default:
		}
		time.Sleep(5 * time.Second)
	}
}

func main() {
	x := new(CX)
	x.initiated = make(chan bool)
	r, err := restarter.NewRestarter(":8008")
	if err != nil {
		fmt.Printf("Exiting: %v\n", err.Error())
		os.Exit(1)
	}
	r.Sub = x
	if err = r.Run(); err != nil {
		fmt.Printf("Exiting: %v\n", err.Error())
		os.Exit(1)
	}
	fmt.Println("Exiting")
}