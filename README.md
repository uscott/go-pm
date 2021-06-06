# go-pm

Simple module for restarting processes

Adapted from https://gravitational.com/blog/golang-ssh-bastion-graceful-restarts/

### Install
```shell script
go get github.com/uscott/go-pm
```

### Usage

> See example directory

```go

import (
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/pkg/errors"
	"github.com/uscott/go-pm"
)

// Struct that implements Subordinate interface in pm.go
type CX struct {
	errorC chan error
	doneC  chan struct{}
	wait   time.Duration
}

func NewCX() *CX {
	return &CX{errorC: make(chan error), doneC: make(chan struct{})}
}

func (x *CX) Done() chan struct{} {
	return x.doneC
}

func (x *CX) Error() chan error {
	return x.errorC
}

func (x *CX) OnFork() { fmt.Println("Fork") }
func (x *CX) OnQuit() { fmt.Println("Quit") }

func (x *CX) WaitTime() time.Duration {
	return x.wait
}

func (x *CX) Run() {

	rand.Seed(time.Now().UnixNano())

	for {

		n := rand.Int31n(20)
		fmt.Println(n)

		switch n {
		case 0:
			x.errorC <- errors.Errorf("n == 0")
		case 19:
			x.doneC <- struct{}{}
		default:
		}
		time.Sleep(time.Second / 10)
	}
}

func main() {

	x := NewCX()
	pm, err := pm.NewProcessManager(":8008")
	if err != nil {
		fmt.Printf("Exiting: %v\n", err.Error())
		os.Exit(1)
	}

	pm.Sub = x

	if err = pm.Run(); err != nil {
		fmt.Printf("Error, exiting: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Exiting")
}
```
