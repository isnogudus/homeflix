// Passkeys on the login and account pages. The server sends WebAuthn
// options as JSON; this script hands them to the browser and posts the
// answer back as a form field.
"use strict";

(function () {
	if (!window.PublicKeyCredential) {
		const err = document.getElementById("passkeyerror");
		if (err && err.dataset.unsupported && document.getElementById("passkeyadd")) {
			err.textContent = err.dataset.unsupported;
			err.hidden = false;
		}
		return;
	}

	function decode(s) {
		s = s.replace(/-/g, "+").replace(/_/g, "/");
		while (s.length % 4) s += "=";
		const bin = atob(s);
		const out = new Uint8Array(bin.length);
		for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
		return out.buffer;
	}

	function encode(buf) {
		const bytes = new Uint8Array(buf);
		let bin = "";
		for (const b of bytes) bin += String.fromCharCode(b);
		return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
	}

	function withIDs(list) {
		return (list || []).map((c) => Object.assign({}, c, { id: decode(c.id) }));
	}

	function creationOptions(pk) {
		if (PublicKeyCredential.parseCreationOptionsFromJSON) {
			return PublicKeyCredential.parseCreationOptionsFromJSON(pk);
		}
		return Object.assign({}, pk, {
			challenge: decode(pk.challenge),
			user: Object.assign({}, pk.user, { id: decode(pk.user.id) }),
			excludeCredentials: withIDs(pk.excludeCredentials),
		});
	}

	function requestOptions(pk) {
		if (PublicKeyCredential.parseRequestOptionsFromJSON) {
			return PublicKeyCredential.parseRequestOptionsFromJSON(pk);
		}
		return Object.assign({}, pk, {
			challenge: decode(pk.challenge),
			allowCredentials: withIDs(pk.allowCredentials),
		});
	}

	function credentialJSON(c) {
		try {
			if (c.toJSON) return JSON.stringify(c.toJSON());
		} catch (e) {
			// Some browsers have toJSON but fail on extensions; encode by hand.
		}
		const r = c.response;
		const response = { clientDataJSON: encode(r.clientDataJSON) };
		if (r.attestationObject) {
			response.attestationObject = encode(r.attestationObject);
			if (r.getTransports) response.transports = r.getTransports();
		}
		if (r.authenticatorData) {
			response.authenticatorData = encode(r.authenticatorData);
			response.signature = encode(r.signature);
			if (r.userHandle) response.userHandle = encode(r.userHandle);
		}
		return JSON.stringify({
			id: c.id,
			rawId: encode(c.rawId),
			type: c.type,
			response: response,
			authenticatorAttachment: c.authenticatorAttachment || undefined,
			clientExtensionResults: c.getClientExtensionResults(),
		});
	}

	async function post(url, fields) {
		const res = await fetch(url, {
			method: "POST",
			body: new URLSearchParams(fields),
			credentials: "same-origin",
		});
		let body = {};
		try {
			body = await res.json();
		} catch (e) {
			// Not JSON: fall back to the status text.
		}
		if (!res.ok) throw new Error(body.error || res.statusText);
		return body;
	}

	// show writes why a passkey did not work; aborted requests stay quiet.
	function show(err, e) {
		if (e && e.name === "AbortError") return;
		let msg = e && e.message ? e.message : err.dataset.failed;
		if (e && e.name === "NotAllowedError") msg = err.dataset.cancelled;
		if (e && e.name === "InvalidStateError" && err.dataset.exists) msg = err.dataset.exists;
		err.textContent = msg;
		err.hidden = false;
	}

	const login = document.getElementById("login");
	const button = document.getElementById("passkeylogin");
	if (login && button) {
		const err = document.getElementById("passkeyerror");
		const next = login.elements.next.value;
		let conditional = null; // the AbortController of the autofill request

		const signIn = async (autofill) => {
			const opts = await post("/web/passkey/begin", { conditional: autofill ? "1" : "0" });
			const ctl = new AbortController();
			if (autofill) conditional = ctl;
			const req = { publicKey: requestOptions(opts.publicKey), signal: ctl.signal };
			if (autofill) req.mediation = "conditional";
			const cred = await navigator.credentials.get(req);
			const res = await post("/web/passkey/finish", {
				ceremony: opts.ceremony,
				credential: credentialJSON(cred),
				next: next,
			});
			location.assign(res.next);
		};

		button.hidden = false;
		button.addEventListener("click", () => {
			if (conditional) conditional.abort();
			conditional = null;
			err.hidden = true;
			signIn(false).catch((e) => show(err, e));
		});
		if (PublicKeyCredential.isConditionalMediationAvailable) {
			PublicKeyCredential.isConditionalMediationAvailable().then((ok) => {
				if (!ok) return;
				signIn(true).catch((e) => {
					if (e.name !== "NotAllowedError") show(err, e);
				});
			});
		}
	}

	const add = document.getElementById("passkeyadd");
	if (add) {
		const err = document.getElementById("passkeyerror");
		const submit = add.querySelector("button");
		submit.disabled = false;
		add.addEventListener("submit", async (ev) => {
			ev.preventDefault();
			err.hidden = true;
			submit.disabled = true;
			try {
				const csrf = add.elements.csrf.value;
				const opts = await post("/web/account/passkeys/begin", { csrf: csrf });
				const cred = await navigator.credentials.create({ publicKey: creationOptions(opts.publicKey) });
				const res = await post("/web/account/passkeys/finish", {
					csrf: csrf,
					ceremony: opts.ceremony,
					credential: credentialJSON(cred),
					name: add.elements.name.value,
				});
				location.assign(res.next);
			} catch (e) {
				show(err, e);
				submit.disabled = false;
			}
		});
	}
})();
