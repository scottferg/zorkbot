package main

import (
	"bytes"
	"encoding/gob"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"bitbucket.org/zombiezen/gonorth/north"
	"github.com/bwmarrin/discordgo"
)

var (
	token string
	debug bool

	channelId string
)

const (
	story = "./zork1.z5"
	path  = "./state.save"

	testChannelId = "439428368291725314"
	prodChannelId = "439455195894906891"
)

func init() {
	flag.StringVar(&token, "t", "", "Bot Token")
	flag.BoolVar(&debug, "d", true, "Debug flag")
	flag.Parse()

	if debug {
		channelId = testChannelId
	} else {
		channelId = prodChannelId
	}
}

func main() {
	buf := newInputBuffer()

	discord, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal(err)
	}

	ui := &discordUI{in: buf, out: newOutputBuffer(discord)}

	f, err := os.Open(story)
	if err != nil {
		log.Fatal(err)
	}

	machine := &north.Machine{}

	err = ui.Restore(machine)
	if err != nil {
		log.Println(err)

		machine, err = north.NewMachine(f, ui)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
	}

	defer ui.Save(machine)

	discord.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == s.State.User.ID {
			return
		}

		if m.ChannelID != channelId {
			return
		}

		if len(m.Content) > 2 && strings.HasPrefix(m.Content, "!z") {
			buf.WriteString(strings.TrimSpace(strings.Split(m.Content, "!z")[1]) + "\n")
		}
	})

	err = discord.Open()
	if err != nil {
		log.Fatal(err)
	}

	defer discord.Close()

	go func() {
		for {
			err = machine.Run()
			switch err {
			case io.EOF, north.ErrQuit:
				os.Exit(0)
			case north.ErrRestart:
				fmt.Fprintln(os.Stderr, "** Restart Error:", err)
				os.Exit(1)
			default:
				fmt.Fprintln(os.Stderr, "** Internal Error:", err)
				os.Exit(1)
			}
		}
	}()

	fmt.Println("Zorkbot is now running.  Press CTRL-C to exit.")

	wait := make(chan os.Signal, 1)
	signal.Notify(wait, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-wait
}

func newInputBuffer() *inputBuffer {
	return &inputBuffer{msg: make(chan bool, 1)}
}

type inputBuffer struct {
	msg chan bool

	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *inputBuffer) WriteString(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf.WriteString(s)

	b.msg <- true
}

func (b *inputBuffer) ReadRune() (rune, int, error) {
	b.mu.Lock()

	length := b.buf.Len()

	b.mu.Unlock()

	if length == 0 {
		<-b.msg
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.ReadRune()
}

type discordUI struct {
	in  io.RuneReader
	out *outputBuffer
}

func (t *discordUI) Input(n int) ([]rune, error) {
	r := make([]rune, 0, n)
	for {
		rr, _, err := t.in.ReadRune()
		if err != nil {
			return r, err
		} else if rr == '\n' {
			break
		}
		if len(r) < n {
			r = append(r, rr)
		}
	}
	return r, nil
}

func (t *discordUI) Output(window int, s string) error {
	if window != 0 {
		return nil
	}

	t.out.WriteString(s)

	return nil
}

func (t *discordUI) ReadRune() (rune, int, error) {
	return t.in.ReadRune()
}

func (t *discordUI) Save(m *north.Machine) error {
	f, err := os.Create(path)
	if err == nil {
		encoder := gob.NewEncoder(f)
		encoder.Encode(m)
	}
	defer f.Close()

	return err
}

func (t *discordUI) Restore(m *north.Machine) error {
	f, err := os.Open(path)
	if err == nil {
		decoder := gob.NewDecoder(f)
		err = decoder.Decode(m)
	}
	defer f.Close()

	return err
}

type outputBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newOutputBuffer(sender sendMessager) *outputBuffer {
	buf := &outputBuffer{}

	go buf.coalesceAndSend(sender)

	return buf
}

func (b *outputBuffer) WriteString(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf.WriteString(s)
}

func (b *outputBuffer) coalesceAndSend(sender sendMessager) {
	for range time.Tick(2 * time.Second) {
		b.mu.Lock()

		line := b.buf.String()
		b.buf.Reset()

		b.mu.Unlock()
		if line == "" {
			continue
		}

		sender.ChannelMessageSend(channelId, line)
	}
}

type sendMessager interface {
	ChannelMessageSend(channel string, s string) (*discordgo.Message, error)
}
