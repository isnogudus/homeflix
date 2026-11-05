// The web player of homeflix: plays the parts of a version one after
// another, starts where the user stopped, and tells the server where it is,
// so that Moonfin and the web client resume at the same place.
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
	let index = cfg.part;
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

	function load(i, at) {
		index = i;
		started = false;
		const part = cfg.parts[i];
		for (const t of video.querySelectorAll("track")) {
			t.remove();
		}
		video.src = part.src;
		for (const t of part.tracks) {
			const el = document.createElement("track");
			el.kind = "subtitles";
			el.label = t.label;
			el.srclang = t.lang || "";
			el.src = t.src;
			el.default = t.default;
			video.appendChild(el);
		}
		partInfo.hidden = cfg.parts.length < 2;
		partInfo.textContent = cfg.partLabel + " " + (i + 1) + " / " + cfg.parts.length;
		video.addEventListener("loadedmetadata", () => {
			if (at > 0 && at < video.duration) {
				video.currentTime = at;
			}
			video.play().catch(() => {});
		}, { once: true });
	}

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
		errorBox.hidden = false;
	});
	addEventListener("pagehide", () => {
		if (started && !video.ended) {
			report("stop", true);
		}
	});

	load(index, cfg.start);
})();
