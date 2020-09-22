package restarter

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/uscott/go-tools/errs"
)

const DefaultAddress string = ":8080"

type Restarter struct {
	Address     string
	NetListener *net.Listener
	Server      *http.Server
	Signal      chan os.Signal
	Sub         Subordinate
}

type Subordinate interface {
	Done() chan bool
	Error() chan error
	Initiate()
	Run()
	WaitTime() time.Duration
}

type Listener struct {
	Address  string `json:"address"`
	FD       int    `json:"fd"`
	Filename string `json:"filename"`
}

func NewRestarter(address string) (*Restarter, error) {
	if address == "" {
		address = DefaultAddress
	}
	var err error
	r := &Restarter{Address: address}
	r.NetListener = new(net.Listener)
	r.Server = new(http.Server)
	r.Signal = make(chan os.Signal, 1024)
	if err = r.CreateOrImportListener(); err != nil {
		return nil, err
	}
	if err = r.StartServer(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Restarter) ImportListener() (*net.Listener, error) {
	// Extract the encoded listener metadata from the environment.
	listenerEnv := os.Getenv("LISTENER")
	if listenerEnv == "" {
		return nil, fmt.Errorf("unable to find LISTENER environment variable")
	}
	// Unmarshal the listener metadata.
	l := Listener{}
	err := json.Unmarshal([]byte(listenerEnv), &l)
	if err != nil {
		return nil, err
	}
	if r.Address == "" {
		r.Address = DefaultAddress
	}
	address := r.Address
	if l.Address != address {
		return nil, fmt.Errorf("unable to find listener for %v", address)
	}
	// The file has already been passed to this process, extract the file
	// descriptor and name from the metadata to rebuild/find the *os.File for
	// the listener.
	listenerFile := os.NewFile(uintptr(l.FD), l.Filename)
	if listenerFile == nil {
		return nil, fmt.Errorf("unable to create listener file: %v", err.Error())
	}
	defer listenerFile.Close()
	// Create a net.Listener from the *os.File.
	ln, err := net.FileListener(listenerFile)
	if err != nil {
		return nil, err
	}
	return &ln, nil
}

func (r *Restarter) CreateListener() (*net.Listener, error) {
	if r.Address == "" {
		r.Address = DefaultAddress
	}
	address := r.Address
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	return &ln, nil
}

func (r *Restarter) CreateOrImportListener() error {
	// Try and import a listener for addr. If it's found, use it.
	r.NetListener = nil
	ln, err := r.ImportListener()
	if err == nil {
		fmt.Printf("Imported listener file descriptor for %v.\n", r.Address)
		r.NetListener = ln
		return nil
	}
	// No listener was imported, that means this process has to create one.
	ln, err = r.CreateListener()
	if err != nil {
		return err
	}
	r.NetListener = ln
	fmt.Printf("Created listener file descriptor for %v\n", r.Address)
	return nil
}

func (r *Restarter) GetListenerFile() (*os.File, error) {
	ln := r.NetListener
	if ln == nil {
		return nil, errs.ErrNilPtr
	}
	switch t := (*ln).(type) {
	case *net.TCPListener:
		return t.File()
	case *net.UnixListener:
		return t.File()
	}
	return nil, fmt.Errorf("unsupported listener: %T", *ln)
}

func (r *Restarter) ForkChild() (*os.Process, error) {
	// Get the file descriptor for the listener and marshal the metadata to pass
	// to the child in the environment.
	lnFile, err := r.GetListenerFile()
	if err != nil {
		return nil, err
	}
	defer lnFile.Close()
	if r.Address == "" {
		r.Address = DefaultAddress
	}
	address := r.Address
	l := Listener{
		Address:  address,
		FD:       3,
		Filename: lnFile.Name(),
	}
	listenerEnv, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	// Pass stdin, stdout, and stderr along with the listener to the child.
	files := []*os.File{
		os.Stdin,
		os.Stdout,
		os.Stderr,
		lnFile,
	}
	// Get current environment and add in the listener to it.
	environment := append(os.Environ(), "LISTENER="+string(listenerEnv))
	// Get current process name and directory.
	execName, err := os.Executable()
	if err != nil {
		return nil, err
	}
	fmt.Printf("Exec Name = %v\n", execName)
	execDir := filepath.Dir(execName)
	// Spawn child process.
	// p, err := os.StartProcess(execName, []string{execName}, &os.ProcAttr{
	p, err := os.StartProcess(execName, os.Args, &os.ProcAttr{
		Dir:   execDir,
		Env:   environment,
		Files: files,
		Sys:   &syscall.SysProcAttr{},
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (r *Restarter) Run() error {
	go r.Sub.Initiate()
	go r.Sub.Run()
	signal.Notify(r.Signal, syscall.SIGHUP, syscall.SIGUSR2, syscall.SIGINT, syscall.SIGQUIT)
	for {
		select {
		case s := <-r.Signal:
			fmt.Printf("%v signal received\n", s)
			switch s {
			case syscall.SIGHUP:
				// Fork a child process.
				w := r.Sub.WaitTime()
				fmt.Printf("Waiting for %v\n", w)
				time.Sleep(w)
				p, err := r.ForkChild()
				if err != nil {
					fmt.Printf("Unable to fork child: %v.\n", err)
					continue
				}
				fmt.Printf("Forked child %v\n", p.Pid)
				// Create a context that will expire in 5 seconds and use this as a
				// timeout to Shutdown.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				r.Sub = nil
				// Return any errors during shutdown.
				return r.Server.Shutdown(ctx)
			case syscall.SIGUSR2:
				// Fork a child process.
				w := r.Sub.WaitTime()
				fmt.Printf("Waiting for %v\n", w)
				time.Sleep(w)
				p, err := r.ForkChild()
				if err != nil {
					fmt.Printf("Unable to fork child: %v\n", err)
					continue
				}
				// Print the PID of the forked process and keep waiting for more signals.
				fmt.Printf("Forked child %v\n", p.Pid)
			case syscall.SIGINT, syscall.SIGQUIT:
				// Create a context that will expire in 5 seconds and use this as a
				// timeout to Shutdown.
				fmt.Println("Shutting down")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				// Return any errors during shutdown.
				return r.Server.Shutdown(ctx)
			}
		case e := <-r.Sub.Error():
			switch e {
			case nil:
			default:
				// Fork a child process.
				fmt.Println(e.Error())
				w := r.Sub.WaitTime()
				fmt.Printf("Waiting for %v\n", w)
				time.Sleep(w)
				p, err := r.ForkChild()
				if err != nil {
					fmt.Printf("Unable to fork child: %v\n", err)
					continue
				}
				fmt.Printf("Forked child %v\n", p.Pid)
				// Create a context that will expire in 5 seconds and use this as a
				// timeout to Shutdown.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				// Return any errors during shutdown.
				return r.Server.Shutdown(ctx)
			}
		case b := <-r.Sub.Done():
			switch b {
			case false:
			default:
				fmt.Println("Done")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				// Return any errors during shutdown.
				return r.Server.Shutdown(ctx)
			}
		}
	}
}

func (r *Restarter) Handler(w http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(w, "Hello from %v!\n", os.Getpid())
}

func (r *Restarter) StartServer() error {
	http.HandleFunc("/hello", r.Handler)
	if r.Address == "" {
		r.Address = DefaultAddress
	}
	r.Server = &http.Server{
		Addr: r.Address,
	}
	var err error
	go func() {
		err = r.Server.Serve(*r.NetListener)
	}()

	return err
}
