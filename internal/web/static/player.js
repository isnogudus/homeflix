// The web player of homeflix: plays the parts of a version one after
// another, starts where the user stopped, lets the user pick the audio
// track, and tells the server where it is, so that Moonfin and the web
// client resume at the same place. Files browsers play as they are come
// directly; others, and other audio tracks, come remuxed as HLS (natively
// in Safari, through hls.js elsewhere).
(() => {
	"use strict";
	const root = document.getElementById("player");
	if (!root) {
		return;
	}
	const cfg = JSON.parse(root.dataset.config);
	const video = root.querySelector("video");
	const partInfo = document.getElementById("partinfo");
	const errorBox = document.getElementById("playerror");
	const audioChoice = document.getElementById("audiochoice");
	const audioSelect = audioChoice.querySelector("select");
	const errorText = errorBox.textContent;
	const nativeHLS = video.canPlayType("application/vnd.apple.mpegurl") !== "";
	let index = cfg.part;
	let audio = null;
	let hls = null;
	let started = false;
	let lastReport = 0;

	// report sends the position; on leaving the page only a beacon gets out.
	function report(event, beacon) {
		const part = cfg.parts[index];
		const body = new URLSearchParams({
			csrf: cfg.csrf, item: part.item, source: part.source, event: event,
			position: String(Math.floor(video.currentTime || 0)),
		});
		if (beacon && navigator.sendBeacon) {
			navigator.sendBeacon("/web/progress", body);
			return;
		}
		fetch("/web/progress", { method: "POST", body: body, credentials: "same-origin", keepalive: true })
			.catch(() => {});
	}

	function showError(text) {
		errorBox.textContent = text || errorText;
		errorBox.hidden = false;
	}

	function defaultAudio(part) {
		const a = part.audio.find((t) => t.default) || part.audio[0];
		return a ? a.index : null;
	}

	// source picks the file itself while the browser can play it with the
	// chosen audio, else the remux of that audio track.
	function source(part, track) {
		if (part.direct && (track === null || track === defaultAudio(part) || !part.hls)) {
			return { url: part.direct, hls: false };
		}
		return { url: part.hls + track + "/index.m3u8", hls: true };
	}

	function attach(src, at) {
		if (hls) {
			hls.destroy();
			hls = null;
		}
		errorBox.hidden = true;
		if (src.hls && !nativeHLS) {
			if (!window.Hls || !window.Hls.isSupported()) {
				showError();
				return;
			}
			hls = new window.Hls({ enableWorker: false, startPosition: at > 0 ? at : -1, maxBufferLength: 60 });
			hls.on(window.Hls.Events.MANIFEST_PARSED, () => {
				video.play().catch(() => {});
			});
			hls.on(window.Hls.Events.ERROR, (_, data) => {
				if (!data.fatal) {
					return;
				}
				const busy = data.response && data.response.code === 503;
				if (busy || data.type === window.Hls.ErrorTypes.NETWORK_ERROR) {
					showError(busy ? cfg.busy : undefined);
					const h = hls;
					setTimeout(() => {
						if (hls === h) {
							errorBox.hidden = true;
							h.startLoad();
						}
					}, 10000);
				} else if (data.type === window.Hls.ErrorTypes.MEDIA_ERROR) {
					hls.recoverMediaError();
				} else {
					showError();
				}
			});
			hls.loadSource(src.url);
			hls.attachMedia(video);
			return;
		}
		video.src = src.url;
		video.addEventListener("loadedmetadata", () => {
			if (at > 0 && at < video.duration) {
				video.currentTime = at;
			}
			video.play().catch(() => {});
		}, { once: true });
	}

	function load(i, at, track) {
		index = i;
		started = false;
		const part = cfg.parts[i];
		audio = track === undefined || track === null ? defaultAudio(part) : track;
		for (const t of video.querySelectorAll("track")) {
			t.remove();
		}
		for (const t of part.tracks || []) {
			const el = document.createElement("track");
			el.kind = "subtitles";
			el.label = t.label;
			el.srclang = t.lang || "";
			el.src = t.src;
			el.default = t.default;
			video.appendChild(el);
		}
		audioSelect.replaceChildren(...part.audio.map((t) => {
			const o = new Option(t.label, String(t.index));
			o.selected = t.index === audio;
			return o;
		}));
		audioChoice.hidden = part.audio.length < 2 || !part.hls;
		partInfo.hidden = cfg.parts.length < 2;
		partInfo.textContent = cfg.partLabel + " " + (i + 1) + " / " + cfg.parts.length;
		attach(source(part, audio), at);
	}

	audioSelect.addEventListener("change", () => {
		const at = video.currentTime;
		report("progress");
		load(index, at, Number(audioSelect.value));
	});
	video.addEventListener("playing", () => {
		errorBox.hidden = true;
		if (!started) {
			started = true;
			report("start");
		}
	});
	video.addEventListener("timeupdate", () => {
		const now = Date.now();
		if (!video.paused && now - lastReport > 10000) {
			lastReport = now;
			report("progress");
		}
	});
	video.addEventListener("pause", () => {
		if (!video.ended) {
			report("progress");
		}
	});
	video.addEventListener("ended", () => {
		report("stop");
		if (index + 1 < cfg.parts.length) {
			load(index + 1, 0);
		} else {
			location.href = cfg.back;
		}
	});
	video.addEventListener("error", () => {
		if (!hls) {
			showError();
		}
	});
	addEventListener("pagehide", () => {
		if (started && !video.ended) {
			report("stop", true);
		}
	});

	load(index, cfg.start);
})();
