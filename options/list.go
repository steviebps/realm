package options

import (
	"container/list"
	"errors"
)

var optionsList = list.New()

// GoBack returns previously OpenOption
func GoBack() (*OpenOption, error) {
	front := optionsList.Front()

	for i := 0; i < 2 && front != nil; i++ {
		optionsList.Remove(front)
		front = optionsList.Front()
	}

	if front == nil {
		return nil, errors.New("Could no go back any further")
	}

	return front.Value.(*OpenOption), nil
}
