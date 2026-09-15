package main

import "fmt"

func main() {
	fmt.Println("wut")
	val3 := add(1, 3)
	if val3 > 10 {
		fmt.Println(val3)
		fmt.Println("greater than 10")
	}
	loop(val3)
}

func add(val1 int, val2 int) int {
	return val1 + val2
}

func loop(val1 int) {
	for index := 0; index < val1; index++ {
		fmt.Println(index)
	}
}

func loopArray([]int) {
	
}