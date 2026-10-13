package rigctl

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestSetPTTCommand(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	commands := make(chan string, 2)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadString('\n')
			commands <- line
			_, _ = fmt.Fprint(conn, "RPRT 0\n")
			_ = conn.Close()
		}
	}()
	client := New(ln.Addr().String())
	client.Timeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.SetPTT(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := <-commands; got != "T 1\n" {
		t.Fatalf("expected TX command, got %q", got)
	}
	if err := client.SetPTT(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := <-commands; got != "T 0\n" {
		t.Fatalf("expected RX command, got %q", got)
	}
}

func TestSetPTTValueResponseUsesDataPTTCommand(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	command := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		command <- line
		_, _ = fmt.Fprint(conn, "RPRT 0\n")
	}()
	client := New(ln.Addr().String())
	client.Timeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.SetPTTValueResponse(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if got := <-command; got != "T 3\n" {
		t.Fatalf("expected data PTT command T 3, got %q", got)
	}
}

func TestPTTSessionKeepsOneConnectionUntilRelease(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type result struct {
		commands []string
		count    int
	}
	resultCh := make(chan result, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			resultCh <- result{}
			return
		}
		defer conn.Close()
		seen := []string{}
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			line := scanner.Text()
			seen = append(seen, line)
			_, _ = fmt.Fprintln(conn, "RPRT 0")
		}
		resultCh <- result{commands: seen, count: 1}
	}()

	client := New(ln.Addr().String())
	client.Timeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, response, err := client.StartPTTSession(ctx, 3)
	if err != nil {
		t.Fatalf("start PTT session: %v", err)
	}
	if response != "RPRT 0" {
		t.Fatalf("start response = %q, want RPRT 0", response)
	}
	// No further command should be necessary to keep the session held.
	time.Sleep(50 * time.Millisecond)
	response, err = session.SetPTT(ctx, 0)
	if err != nil {
		t.Fatalf("release PTT session: %v", err)
	}
	if response != "RPRT 0" {
		t.Fatalf("release response = %q, want RPRT 0", response)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close PTT session: %v", err)
	}

	select {
	case got := <-resultCh:
		if got.count != 1 || len(got.commands) != 2 || got.commands[0] != "T 3" || got.commands[1] != "T 0" {
			t.Fatalf("PTT session result = %#v, want one connection with [T 3 T 0]", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for persistent PTT session commands")
	}
}
