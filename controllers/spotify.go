package controllers

import (
	log "github.com/sirupsen/logrus"
	"github.com/zmb3/spotify"
	"jamfactory-backend/types"
	"jamfactory-backend/utils"
	"net/http"
	"strings"
)

var (
	spotifySearchCacheKey = utils.RedisKey{}.Append("search")
)

func devices(w http.ResponseWriter, r *http.Request) {
	jamSession := utils.JamSessionFromRequestContext(r)

	result, err := jamSession.Client.PlayerDevices()

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.WithField("JamSession", jamSession.Label).Debug("Could not get devices for jamSession: ", err.Error())
		return
	}

	res := types.GetSpotifyDevicesResponse{
		Devices: result,
	}

	utils.EncodeJSONBody(w, res)
}

func playlist(w http.ResponseWriter, r *http.Request) {
	jamSession := utils.JamSessionFromRequestContext(r)

	result, err := jamSession.Client.CurrentUsersPlaylists()

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.WithField("JamSession", jamSession.Label).Debug("Could not get playlists for jamSession: ", err.Error())
		return
	}

	res := types.GetSpotifyPlaylistsResponse{
		Playlists: result,
	}

	utils.EncodeJSONBody(w, res)
}

func search(w http.ResponseWriter, r *http.Request) {
	jamSession := utils.JamSessionFromRequestContext(r)

	var body types.PutSpotifySearchRequest
	if err := utils.DecodeJSONBody(w, r, &body); err != nil {
		return
	}

	country := spotify.CountryGermany
	opts := spotify.Options{
		Country: &country,
	}
	var searchType spotify.SearchType
	switch body.SearchType {
	case "track":
		searchType = spotify.SearchTypeTrack
	case "playlist":
		searchType = spotify.SearchTypePlaylist
	case "album":
		searchType = spotify.SearchTypeAlbum
	}

	if searchType == 0 {
		http.Error(w, "Unsupported search type", http.StatusUnprocessableEntity)
		log.WithFields(log.Fields{
			"JamSession": jamSession.Label,
			"Text":       body.SearchText}).Debug("Unsupported search type: ", body.SearchType)
		return
	}

	searchString := []string{body.SearchText, "*"}

	entry, err := cache.Query(spotifySearchCacheKey, strings.Join(searchString, ""),
		func(index string) (interface{}, error) { return jamSession.Client.SearchOpt(index, searchType, &opts) })

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.WithFields(log.Fields{
			"JamSession": jamSession.Label,
			"Text":       body.SearchText}).Debug("Could not get query cache: ", err.Error())
		return
	}

	if result, ok := entry.(*spotify.SearchResult); ok {
		res := types.PutSpotifySearchResponse{
			Artists:   result.Artists,
			Albums:    result.Albums,
			Playlists: result.Playlists,
			Tracks:    result.Tracks,
		}
		utils.EncodeJSONBody(w, res)
	} else {
		http.Error(w, "Could not cast cache response to corresponding struct", http.StatusInternalServerError)
		log.Warn("Could not cast cache response to corresponding struct")
		return
	}
}
