package store

import "github.com/yyydddkkk/SwarmJet/internal/domain"

// NotificationTarget routes platform notices to an enabled Queen, otherwise
// to the dispatcher. Without a readable dispatcher, notices stay in the Hive.
func NotificationTarget(hive domain.Hive, dispatcher domain.Address) domain.Address {
	if hive.QueenEnabled {
		return domain.AddressQueen
	}
	if dispatcher == "" || dispatcher == domain.AddressSystem {
		return domain.AddressHive
	}
	return dispatcher
}
