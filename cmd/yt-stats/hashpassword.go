package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"yt-stats/internal/auth"
)

// secretReader wypisuje zachętę i czyta jedno hasło.
type secretReader func(prompt string) ([]byte, error)

func cmdHashPassword(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "hash-password nie przyjmuje argumentów; hasło jest czytane z terminala")
		return 2
	}
	hash, err := hashPasswordInteractive(newSecretReader(stdin, stderr))
	if err != nil {
		fmt.Fprintf(stderr, "hash-password: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, hash)
	return 0
}

// hashPasswordInteractive czyta hasło dwa razy i zwraca hash Argon2id.
func hashPasswordInteractive(read secretReader) (string, error) {
	first, err := read("Hasło: ")
	if err != nil {
		return "", err
	}
	defer clear(first)
	second, err := read("Powtórz hasło: ")
	if err != nil {
		return "", err
	}
	defer clear(second)

	if !bytes.Equal(first, second) {
		return "", errors.New("hasła się różnią")
	}
	return auth.HashPassword(first, auth.DefaultParams)
}

// newSecretReader czyta bez echa z terminala. Gdy wejście nie jest
// terminalem (np. potok w skrypcie), czyta kolejne wiersze.
func newSecretReader(stdin io.Reader, prompt io.Writer) secretReader {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fd := int(f.Fd())
		return func(p string) ([]byte, error) {
			fmt.Fprint(prompt, p)
			b, err := term.ReadPassword(fd)
			fmt.Fprintln(prompt)
			if err != nil {
				return nil, fmt.Errorf("odczyt hasła: %w", err)
			}
			return b, nil
		}
	}
	br := bufio.NewReader(stdin)
	return func(p string) ([]byte, error) {
		fmt.Fprint(prompt, p)
		line, err := br.ReadBytes('\n')
		if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
			return nil, errors.New("odczyt hasła: brak danych na wejściu")
		}
		fmt.Fprintln(prompt)
		return bytes.TrimRight(line, "\r\n"), nil
	}
}
