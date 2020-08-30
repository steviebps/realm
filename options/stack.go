package options

// Stack
type Stack []OpenOption

// IsEmpty check if stack is empty
func (s *Stack) IsEmpty() bool {
	return len(*s) == 0
}

// Push add a new value onto the stack
func (s *Stack) Push(option OpenOption) {
	*s = append(*s, option)
}

// Pop Remove and return top element of stack. Return false if stack is empty.
func (s *Stack) Pop() (*OpenOption, bool) {
	if s.IsEmpty() {
		return nil, false
	} else {
		index := len(*s) - 1
		element := (*s)[index]
		*s = (*s)[:index]
		return &element, true
	}
}
