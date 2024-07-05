package static

import "embed"

// go :embed openseadragon/images/* openseadragon/openseadragon.min.js openseadragon/openseadragon.min.js.map
// go :embed videojs/video-js.min.css videojs/video.min.js

//go:embed replayweb/js/sw.js replayweb/js/ui.js
var FS embed.FS
