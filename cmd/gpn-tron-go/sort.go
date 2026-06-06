package main

import "sort"

func sortPlayers(players []*Player) {
	sort.Slice(players, func(i, j int) bool { return players[i].Username < players[j].Username })
}
