package music

import (
	"log"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/sdl_mixer"
)

// Load SDL
func Init() {
	if err := sdl.Init(sdl.INIT_AUDIO); err != nil {
		log.Println(err)
		return
	}

	if err := mix.Init(mix.INIT_MP3); err != nil {
		log.Println(err)
		return
	}

	// Default: 22050, mix.DEFAULT_FORMAT, 2, 4096
	// we want 44.1 kHz/16 bit quality for our songs
	if err := mix.OpenAudio(44100, mix.DEFAULT_FORMAT, 2, 4096); err != nil {
		log.Println(err)
		return
	}
}

// Teardown SDL
func Quit() {
	mix.CloseAudio()
	mix.Quit()
	sdl.Quit()
}
