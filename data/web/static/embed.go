package static

import "embed"

// go :embed openseadragon/images/* openseadragon/openseadragon.min.js openseadragon/openseadragon.min.js.map
// go :embed videojs/video-js.min.css videojs/video.min.js

//go:embed replayweb/js/sw.js replayweb/js/ui.js
//go:embed foliate-js/*.js foliate-js/ui/*.js foliate-js/vendor/*.js
var FS embed.FS
