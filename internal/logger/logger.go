package logger

import (
	"fmt"

	"github.com/steviebps/realm/internal/colors"
)

func ErrorString(e string) {
	fmt.Println(colors.Red(e))
}

func Error(e error) {
	fmt.Println(colors.Red(e))
}

func ErrorWithPrefix(prefix string) func(string) {
	return func(e string) {
		ErrorString(prefix + e)
	}
}

func InfoString(i string) {
	fmt.Println(colors.Teal(i))
}
