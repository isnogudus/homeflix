// The audio player of homeflix: one bar at the bottom of every page that
// plays music and audiobooks from a queue, tells the server where it is,
// and keeps playing while the listener moves on: while something plays,
// links and forms of the web client load their page into the content area
// instead of replacing the whole page. Files browsers play come directly;
// others come converted to AAC as HLS (natively in Safari, through hls.js
// elsewhere).
(() => {
	"use strict";
	const bar = document.getElementById("audioplayer");
	if (!bar) {
		return;
	}
	const audio = bar.querySelector("audio");
	const $ = (sel) => bar.querySelector(sel);
	const art = $(".art"), artImg = $(".art img"), titleLink = $(".title"), artistEl = $(".artist");
	const toggleBtn = $(".playpause"), seek = $(".seek input"), posEl = $(".pos"), lenEl = $(".len");
	const chapterSel = $(".chapter"), speedSel = $(".speed"), sleepSel = $(".sleep");
	const queueList = $(".queue"), errorBox = $(".flash");
	const csrf = bar.dataset.csrf;
	const nativeHLS = audio.canPlayType("application/vnd.apple.mpegurl") !== "";

	let queue = [];
	let index = -1;
	let hls = null;
	let started = false, moved = false, lastReport = 0, sleepAt = 0;
	// generation counts attach calls; handlers of an older one do nothing.
	let generation = 0;
	let pendingBegin = null;
	const store = window.sessionStorage;

	function fmt(s) {
		s = Math.max(0, Math.floor(s || 0));
		const h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60, sec = s % 60;
		const mm = h ? String(m).padStart(2, "0") : String(m);
		return (h ? h + ":" : "") + mm + ":" + String(sec).padStart(2, "0");
	}

	function current() { return queue[index]; }
	function active() { return index >= 0 && !!current(); }

	function report(event, beacon) {
		const t = current();
		if (!t || (event !== "start" && !moved)) {
			return;
		}
		const body = new URLSearchParams({
			csrf: csrf, item: t.item, source: t.item, event: event,
			position: String(Math.floor(audio.currentTime || 0)),
		});
		if (beacon && navigator.sendBeacon) {
			navigator.sendBeacon("/web/progress", body);
			return;
		}
		fetch("/web/progress", { method: "POST", body: body, credentials: "same-origin", keepalive: true }).catch(() => {});
	}

	function save() {
		try {
			store.setItem("homeflix.audio", JSON.stringify({ queue: queue, index: index, at: audio.currentTime || 0 }));
		} catch (e) { /* storage full or off */ }
	}

	function loadHLS() {
		if (window.Hls) {
			return Promise.resolve();
		}
		return new Promise((ok, fail) => {
			const s = document.createElement("script");
			s.src = bar.dataset.hls;
			s.onload = ok;
			s.onerror = fail;
			document.head.appendChild(s);
		});
	}

	function detach() {
		generation++;
		if (pendingBegin) {
			audio.removeEventListener("loadedmetadata", pendingBegin);
			pendingBegin = null;
		}
		if (hls) {
			hls.destroy();
			hls = null;
		}
	}

	// attach starts track t at second at; play says whether to start
	// playing (a restored queue waits for the listener).
	async function attach(t, at, play) {
		detach();
		errorBox.hidden = true;
		posEl.textContent = fmt(at);
		if (t.duration > 0) {
			seek.value = String(Math.round(1000 * (at || 0) / t.duration));
		}
		const gen = generation;
		const direct = t.direct && t.mime && audio.canPlayType(t.mime) !== "";
		const begin = () => {
			pendingBegin = null;
			if (gen !== generation) {
				return;
			}
			if (at > 0) {
				audio.currentTime = at;
			}
			if (play) {
				audio.play().catch(() => {});
			}
		};
		if (direct) {
			audio.src = t.direct;
		} else if (t.hls && nativeHLS) {
			audio.src = t.hls;
		} else if (t.hls) {
			try {
				await loadHLS();
			} catch (e) {
				showError();
				return;
			}
			if (gen !== generation) {
				return; // another track was chosen meanwhile
			}
			if (!window.Hls.isSupported()) {
				showError();
				return;
			}
			hls = new window.Hls({ enableWorker: false, startPosition: at > 0 ? at : -1, maxBufferLength: 60 });
			hls.on(window.Hls.Events.ERROR, (_, data) => {
				if (data.fatal) {
					if (data.type === window.Hls.ErrorTypes.MEDIA_ERROR) {
						hls.recoverMediaError();
					} else {
						showError();
					}
				}
			});
			hls.loadSource(t.hls);
			hls.attachMedia(audio);
			at = 0;
			if (play) {
				audio.play().catch(() => {});
			}
			return;
		} else {
			showError();
			return;
		}
		pendingBegin = begin;
		audio.addEventListener("loadedmetadata", begin, { once: true });
		if (play) {
			// Asked at once, so the click counts as the wish to play.
			audio.play().catch(() => {});
		}
	}

	function showError() {
		errorBox.hidden = false;
	}

	function render() {
		const t = current();
		bar.hidden = !t;
		document.body.classList.toggle("withplayer", !!t);
		if (!t) {
			return;
		}
		bar.classList.toggle("book", !!t.book);
		artImg.src = t.cover || "";
		artImg.hidden = !t.cover;
		for (const a of [art, titleLink]) {
			if (t.albumUrl) {
				a.href = t.albumUrl;
			} else {
				a.removeAttribute("href");
			}
		}
		titleLink.textContent = t.title;
		artistEl.textContent = [t.artist, t.album].filter(Boolean).join(" · ");
		lenEl.textContent = fmt(t.duration);
		chapterSel.replaceChildren(...(t.chapters || []).map((c, i) => new Option(c.name, String(i))));
		chapterSel.hidden = !(t.chapters && t.chapters.length);
		queueList.replaceChildren(...queue.map((q, i) => {
			const li = document.createElement("li");
			const b = document.createElement("button");
			b.type = "button";
			b.textContent = q.title + (q.artist ? " – " + q.artist : "");
			b.addEventListener("click", () => load(i, 0, true));
			if (i === index) {
				li.setAttribute("aria-current", "true");
			}
			li.appendChild(b);
			return li;
		}));
		document.title = document.title.replace(/^▶ /, "");
	}

	function load(i, at, play) {
		if (started && !audio.ended) {
			report("stop");
		}
		index = i;
		started = false;
		moved = false;
		const t = current();
		render();
		if (!t) {
			detach();
			audio.removeAttribute("src");
			audio.load();
			save();
			return;
		}
		attach(t, at || 0, play);
		save();
	}

	function setQueue(list, i, at) {
		queue = list.slice();
		load(i, at, true);
	}

	function shuffled(list) {
		const a = list.slice();
		for (let i = a.length - 1; i > 0; i--) {
			const j = Math.floor(Math.random() * (i + 1));
			[a[i], a[j]] = [a[j], a[i]];
		}
		return a;
	}

	// bind wires the play buttons of the content area to the queue given in
	// the data-queue attribute of the nearest element that has one.
	function bind(root) {
		root.querySelectorAll("[data-play],[data-shuffle],[data-enqueue],[data-next]").forEach((b) => {
			if (b.dataset.bound) {
				return;
			}
			b.dataset.bound = "1";
			// The queue of the nearest section, else the page's first.
			const holder = b.closest("[data-queue]") || root.querySelector("[data-queue]") ||
				document.querySelector("main [data-queue]");
			if (!holder) {
				return;
			}
			b.addEventListener("click", (e) => {
				e.preventDefault();
				let list;
				try {
					list = JSON.parse(holder.dataset.queue || "[]");
				} catch (err) {
					return;
				}
				if (!list.length) {
					return;
				}
				if (b.dataset.shuffle !== undefined) {
					setQueue(shuffled(list), 0, 0);
				} else if (b.dataset.enqueue !== undefined) {
					const wasEmpty = !active();
					queue = queue.concat(list);
					if (wasEmpty) {
						load(0, 0, true);
					} else {
						render();
						save();
					}
				} else if (b.dataset.next !== undefined) {
					const t = list[Number(b.dataset.next)];
					if (!active()) {
						setQueue([t], 0, 0);
					} else {
						queue.splice(index + 1, 0, t);
						render();
						save();
					}
				} else {
					const i = Number(b.dataset.play) || 0;
					const at = b.dataset.t !== undefined ? Number(b.dataset.t) : (list[i].start || 0);
					setQueue(list, i, at);
				}
			});
		});
	}

	bar.addEventListener("click", (e) => {
		const b = e.target.closest("[data-act]");
		if (!b) {
			return;
		}
		switch (b.dataset.act) {
		case "toggle":
			if (audio.paused) {
				audio.play().catch(() => {});
			} else {
				audio.pause();
			}
			break;
		case "prev":
			if (audio.currentTime > 5 || index === 0) {
				audio.currentTime = 0;
			} else {
				load(index - 1, 0, true);
			}
			break;
		case "next":
			if (index + 1 < queue.length) {
				load(index + 1, 0, true);
			}
			break;
		case "back":
			audio.currentTime = Math.max(0, audio.currentTime - 30);
			break;
		case "fwd":
			audio.currentTime = Math.min(audio.duration || Infinity, audio.currentTime + 30);
			break;
		case "queue":
			queueList.hidden = !queueList.hidden;
			break;
		case "close":
			audio.pause();
			report("stop");
			queue = [];
			load(-1);
			try {
				store.removeItem("homeflix.audio");
			} catch (err) { /* off */ }
			break;
		}
	});
	seek.addEventListener("input", () => {
		const d = audio.duration || (current() && current().duration) || 0;
		if (d > 0 && isFinite(d)) {
			audio.currentTime = d * Number(seek.value) / 1000;
		}
	});
	chapterSel.addEventListener("change", () => {
		const c = current().chapters[Number(chapterSel.value)];
		if (c) {
			audio.currentTime = c.at;
		}
	});
	speedSel.addEventListener("change", () => {
		audio.playbackRate = Number(speedSel.value) || 1;
	});
	sleepSel.addEventListener("change", () => {
		sleepAt = sleepSel.value ? Date.now() + Number(sleepSel.value) * 60000 : 0;
	});

	audio.addEventListener("playing", () => {
		errorBox.hidden = true;
		toggleBtn.textContent = "⏸";
		if (current() && current().book) {
			audio.playbackRate = Number(speedSel.value) || 1;
		}
		if (!started) {
			started = true;
			report("start");
		}
	});
	audio.addEventListener("pause", () => {
		toggleBtn.textContent = "▶";
		if (!audio.ended) {
			report("progress");
			save();
		}
	});
	audio.addEventListener("timeupdate", () => {
		const t = current();
		if (!t) {
			return;
		}
		if (started && !audio.paused && !audio.seeking && audio.currentTime > 0) {
			moved = true;
		}
		const d = audio.duration && isFinite(audio.duration) ? audio.duration : t.duration;
		posEl.textContent = fmt(audio.currentTime);
		if (d > 0 && document.activeElement !== seek) {
			seek.value = String(Math.round(1000 * audio.currentTime / d));
		}
		if (t.chapters && t.chapters.length) {
			let c = 0;
			while (c + 1 < t.chapters.length && t.chapters[c + 1].at <= audio.currentTime) {
				c++;
			}
			if (chapterSel.value !== String(c)) {
				chapterSel.value = String(c);
			}
		}
		const now = Date.now();
		if (sleepAt && now >= sleepAt) {
			sleepAt = 0;
			sleepSel.value = "";
			audio.pause();
		}
		if (!audio.paused && now - lastReport > 10000) {
			lastReport = now;
			report("progress");
			save();
		}
	});
	audio.addEventListener("ended", () => {
		const t = current();
		if (t && t.duration > 0 && audio.currentTime < t.duration - 30) {
			showError();
			return;
		}
		report("stop");
		started = false;
		if (index + 1 < queue.length) {
			load(index + 1, 0, true);
		} else {
			toggleBtn.textContent = "▶";
		}
	});
	audio.addEventListener("error", () => {
		if (!hls) {
			showError();
		}
	});
	addEventListener("pagehide", () => {
		if (started && !audio.ended) {
			report("stop", true);
		}
		save();
	});

	// Moving on while music plays: pages of the web client are loaded into
	// the content area. The player page, downloads and sign-out load as
	// usual; so does everything while nothing plays.
	function softURL(u) {
		// Pages that bring scripts of their own (the movie player, passkeys)
		// load as usual.
		return u.origin === location.origin && u.pathname.startsWith("/web/") &&
			!/^\/web\/(play|stream|download|logout|login|audio|hls|subtitles|movies\/|account)\b/.test(u.pathname);
	}

	async function swap(resp, push) {
		const html = await resp.text();
		const doc = new DOMParser().parseFromString(html, "text/html");
		const main = doc.querySelector("main");
		if (!main || !resp.ok && resp.status >= 500) {
			location.href = resp.url;
			return;
		}
		document.title = doc.title;
		document.querySelector("main").replaceWith(document.adoptNode(main));
		const header = doc.querySelector("header.bar");
		if (header) {
			document.querySelector("header.bar").replaceWith(document.adoptNode(header));
		}
		if (push) {
			history.pushState({ soft: true }, "", resp.url);
		}
		scrollTo(0, 0);
		bind(document.querySelector("main"));
	}

	async function navigate(href, push) {
		try {
			const resp = await fetch(href, { credentials: "same-origin" });
			await swap(resp, push);
		} catch (e) {
			location.href = href;
		}
	}

	document.addEventListener("click", (e) => {
		if (!active() || audio.paused || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) {
			return;
		}
		const a = e.target.closest("a[href]");
		if (!a || a.target || a.hasAttribute("download") || bar.contains(a) && !a.matches(".title,.art")) {
			return;
		}
		const u = new URL(a.href, location.href);
		if (!softURL(u) || u.hash && u.pathname === location.pathname) {
			return;
		}
		e.preventDefault();
		navigate(u.href, true);
	});
	document.addEventListener("submit", async (e) => {
		const form = e.target;
		if (!active() || audio.paused || e.defaultPrevented) {
			return;
		}
		const u = new URL(form.action, location.href);
		if (!softURL(u)) {
			return;
		}
		e.preventDefault();
		try {
			if ((form.method || "get").toLowerCase() === "get") {
				u.search = new URLSearchParams(new FormData(form)).toString();
				await navigate(u.href, true);
				return;
			}
			const resp = await fetch(u.href, { method: "POST", body: new URLSearchParams(new FormData(form)), credentials: "same-origin" });
			await swap(resp, true);
		} catch (err) {
			form.submit();
		}
	});
	addEventListener("popstate", () => {
		if (active() && !audio.paused) {
			navigate(location.href, false);
		} else {
			location.reload();
		}
	});

	// A movie starting on the page pauses the music.
	document.addEventListener("play", (e) => {
		if (e.target !== audio && e.target.tagName === "VIDEO") {
			audio.pause();
		}
	}, true);

	// A queue from before a full page load comes back paused.
	try {
		const saved = JSON.parse(store.getItem("homeflix.audio") || "null");
		if (saved && saved.queue && saved.queue.length && saved.index >= 0) {
			queue = saved.queue;
			index = saved.index;
			render();
			attach(current(), saved.at || 0, false);
		}
	} catch (e) { /* nothing kept */ }
	bind(document);
})();
