// The web player of homeflix: plays the parts of a version one after
// another, starts where the user stopped, lets the user pick the audio
// track and the subtitles, and tells the server where it is, so that
// Moonfin and the web client resume at the same place. Files browsers play
// as they are come directly; others, and other audio tracks, come as HLS
// (natively in Safari, through hls.js elsewhere), remuxed or, for video
// browsers cannot decode, transcoded. Text subtitles are tracks of the
// video element; image subtitles are burnt into a transcode.
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
	const subChoice = document.getElementById("subchoice");
	const subSelect = subChoice.querySelector("select");
	const errorText = errorBox.textContent;
	// HLS goes through hls.js wherever it works, also in browsers that play
	// HLS themselves (Safari, Chrome since 2025): it tells a busy server from
	// a broken stream and tries again. The browser's own HLS is the fallback.
	const hlsJS = window.Hls && window.Hls.isSupported();
	const nativeHLS = !hlsJS && video.canPlayType("application/vnd.apple.mpegurl") !== "";
	let index = cfg.part;
	let audio = null;
	let burn = null; // stream index of the image subtitle burnt in
	let hls = null;
	let started = false; // the part has played
	let moved = false; // and its position is worth keeping
	let lastReport = 0;

	// report sends the position; on leaving the page only a beacon gets out.
	// Until the part has really played, only its start is reported: a
	// position of 0 from a player still loading would wipe out where the
	// user stopped.
	function report(event, beacon) {
		if (event !== "start" && !moved) {
			return;
		}
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

	// defaultBurn is the image subtitle burnt in without a choice: the
	// default one, but only when the video is transcoded anyway.
	function defaultBurn(part) {
		const b = part.video && (part.burn || []).find((t) => t.default);
		return b ? b.index : null;
	}

	function defaultAudio(part) {
		const a = part.audio.find((t) => t.default) || part.audio[0];
		return a ? a.index : null;
	}

	// source picks the file itself while the browser can play it with the
	// chosen audio, else the remux of that audio track, or a transcode when
	// the video needs it or an image subtitle is burnt in.
	function source(part, track, sub) {
		const transcode = part.video || sub !== null;
		if (!transcode && part.direct && (track === null || track === defaultAudio(part) || !part.hls)) {
			return { url: part.direct, hls: false };
		}
		let variant = String(track);
		if (transcode) {
			variant += "t" + (sub === null ? "" : sub);
		}
		return { url: part.hls + variant + "/index.m3u8", hls: true };
	}

	// showText shows text track i of the video element, -1 for none.
	function showText(i) {
		Array.from(video.textTracks).forEach((t, j) => {
			t.mode = j === i ? "showing" : "disabled";
		});
	}

	// syncSubtitles shows in the choice what plays: the subtitle burnt in,
	// else the text track showing, which the browser's own menu may have
	// changed.
	function syncSubtitles() {
		if (burn !== null) {
			subSelect.value = "b" + burn;
			return;
		}
		const i = Array.from(video.textTracks).findIndex((t) => t.mode === "showing");
		subSelect.value = i < 0 ? "" : "t" + i;
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
				const busy = data.response && data.response.code === 503;
				if (!data.fatal) {
					// hls.js tries again by itself; say why it waits.
					if (busy) {
						showError(cfg.busy);
					}
					return;
				}
				if (busy || data.type === window.Hls.ErrorTypes.NETWORK_ERROR) {
					showError(busy ? cfg.busy : undefined);
					const h = hls;
					setTimeout(() => {
						if (hls === h) {
							errorBox.hidden = true;
							// Where the player is, or where it was to start.
							h.startLoad(video.currentTime > 0 ? video.currentTime : (at > 0 ? at : -1));
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
			// Asked at once, so that the click that opened the player counts
			// as the user's wish to play.
			video.play().catch(() => {});
			return;
		}
		video.src = src.url;
		video.addEventListener("loadedmetadata", () => {
			if (at > 0 && at < video.duration) {
				video.currentTime = at;
			}
		}, { once: true });
		video.play().catch(() => {});
	}

	function load(i, at, track, sub) {
		index = i;
		started = false;
		moved = false;
		const part = cfg.parts[i];
		audio = track === undefined || track === null ? defaultAudio(part) : track;
		burn = sub === undefined ? defaultBurn(part) : sub;
		for (const t of video.querySelectorAll("track")) {
			t.remove();
		}
		for (const t of part.tracks || []) {
			const el = document.createElement("track");
			el.kind = "subtitles";
			el.label = t.label;
			el.srclang = t.lang || "";
			el.src = t.src;
			el.default = t.default && burn === null;
			video.appendChild(el);
		}
		audioSelect.replaceChildren(...part.audio.map((t) => {
			const o = new Option(t.label, String(t.index));
			o.selected = t.index === audio;
			return o;
		}));
		audioChoice.hidden = part.audio.length < 2 || !part.hls;
		const subs = [new Option(cfg.noSubtitles, "")];
		(part.tracks || []).forEach((t, j) => subs.push(new Option(t.label, "t" + j)));
		for (const b of part.burn || []) {
			subs.push(new Option(b.label + " (" + cfg.burnLabel + ")", "b" + b.index));
		}
		subSelect.replaceChildren(...subs);
		subChoice.hidden = subs.length < 2;
		if (burn !== null) {
			showText(-1);
		}
		syncSubtitles();
		partInfo.hidden = cfg.parts.length < 2;
		partInfo.textContent = cfg.partLabel + " " + (i + 1) + " / " + cfg.parts.length;
		attach(source(part, audio, burn), at);
	}

	audioSelect.addEventListener("change", () => {
		const at = video.currentTime;
		report("progress");
		load(index, at, Number(audioSelect.value), burn);
	});
	subSelect.addEventListener("change", () => {
		const v = subSelect.value;
		showText(v.startsWith("t") ? Number(v.slice(1)) : -1);
		const sub = v.startsWith("b") ? Number(v.slice(1)) : null;
		if (sub !== burn) {
			// Burning in or out needs another stream from the server.
			const at = video.currentTime;
			report("progress");
			load(index, at, audio, sub);
		}
	});
	video.textTracks.addEventListener("change", syncSubtitles);
	video.addEventListener("playing", () => {
		errorBox.hidden = true;
		if (!started) {
			started = true;
			report("start");
		}
	});
	video.addEventListener("timeupdate", () => {
		if (started && !video.paused && !video.seeking && video.currentTime > 0) {
			moved = true;
		}
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
		const part = cfg.parts[index];
		if (part.duration > 0 && video.currentTime < part.duration - 120) {
			// Not the end of the movie: the stream broke off.
			showError();
			return;
		}
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

	if (root.dataset.inline === undefined) {
		load(index, cfg.start);
		return;
	}
	// On a movie page the player opens in place when a play link is
	// clicked, and that click starts it; links opened otherwise, as in a new
	// tab, lead to the player page.
	const box = document.getElementById("inlineplayer");
	for (const a of document.querySelectorAll("a[data-play]")) {
		a.addEventListener("click", (e) => {
			if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) {
				return;
			}
			e.preventDefault();
			if (started) {
				report("progress");
			}
			box.hidden = false;
			load(Number(a.dataset.part) || 0, Number(a.dataset.t) || 0);
			box.scrollIntoView({ block: "start", behavior: "smooth" });
		});
	}
})();
