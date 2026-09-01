import * as THREE from 'three';

const STEP = 1 / 60;
const EYE_HEIGHT = 1.6;

const scene = new THREE.Scene();
scene.background = new THREE.Color(0x0d1117);
scene.fog = new THREE.Fog(0x0d1117, 20, 90);

const camera = new THREE.PerspectiveCamera(75, 1, 0.05, 300);
camera.rotation.order = 'YXZ';

const renderer = new THREE.WebGLRenderer({ antialias: true });
renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
renderer.shadowMap.enabled = true;
renderer.shadowMap.type = THREE.PCFSoftShadowMap;
document.getElementById('viewport').appendChild(renderer.domElement);

scene.add(new THREE.HemisphereLight(0xdce6ff, 0x1a2028, 1.0));
const sun = new THREE.DirectionalLight(0xffffff, 2.0);
sun.position.set(12, 24, 10);
sun.castShadow = true;
sun.shadow.mapSize.set(2048, 2048);
sun.shadow.camera.left = -30;
sun.shadow.camera.right = 30;
sun.shadow.camera.top = 30;
sun.shadow.camera.bottom = -30;
sun.shadow.camera.far = 100;
scene.add(sun);

const grid = new THREE.GridHelper(40, 40, 0x30363d, 0x1b2027);
scene.add(grid);

const COLORS = {
	wall: 0x6e7681,
	box: 0xd08a4e,
	target: 0xe5484d,
	enemy: 0xc42b3d,
	projectile: 0xffe066,
};

function materialFor(b) {
	if (b.projectile) return new THREE.MeshBasicMaterial({ color: COLORS.projectile });
	if (b.enemy) return new THREE.MeshStandardMaterial({ color: COLORS.enemy, roughness: 0.5, metalness: 0.1 });
	if (b.target) return new THREE.MeshStandardMaterial({ color: COLORS.target, emissive: 0x550000, roughness: 0.35, metalness: 0.1 });
	if (b.static) return new THREE.MeshStandardMaterial({ color: COLORS.wall, roughness: 0.9, metalness: 0.0 });
	return new THREE.MeshStandardMaterial({ color: COLORS.box, roughness: 0.65, metalness: 0.05 });
}

function makeMesh(b) {
	let geo;
	if (b.type === 2) {
		const r = Math.max(0.05, b.size[0]);
		const half = Math.max(0.05, b.size[1]);
		geo = new THREE.CapsuleGeometry(r, half * 2, 8, 18);
	} else if (b.type === 1) {
		const r = Math.max(0.05, b.size[0]);
		geo = new THREE.SphereGeometry(r, b.projectile ? 10 : 28, b.projectile ? 8 : 18);
	} else {
		const hx = Math.max(0.05, b.size[0]);
		const hy = Math.max(0.05, b.size[1]);
		const hz = Math.max(0.05, b.size[2]);
		geo = new THREE.BoxGeometry(hx * 2, hy * 2, hz * 2);
	}
	const mesh = new THREE.Mesh(geo, materialFor(b));
	mesh.castShadow = true;
	mesh.receiveShadow = true;
	scene.add(mesh);
	return mesh;
}

const meshes = new Map();

function signature(b) {
	return `${b.type}|${b.static}|${b.target}|${b.enemy}|${b.projectile}|${b.size[0]}`;
}

function applyState(s) {
	const bodies = s.bodies || [];
	updateScene(bodies);
	playerPos = s.player ? s.player.pos : playerPos;
	updateHud(s);
	detectProjectileImpacts(bodies);
}

function updateScene(bodies) {
	const seen = new Set();
	for (const b of bodies) {
		seen.add(b.id);
		let entry = meshes.get(b.id);
		const sig = signature(b);
		if (!entry) {
			const mesh = makeMesh(b);
			entry = { mesh, sig };
			meshes.set(b.id, entry);
		} else if (entry.sig !== sig) {
			entry.mesh.geometry.dispose();
			entry.mesh.material.dispose();
			scene.remove(entry.mesh);
			const mesh = makeMesh(b);
			entry = { mesh, sig };
			meshes.set(b.id, entry);
		}
		entry.mesh.position.set(b.pos[0], b.pos[1], b.pos[2]);
		entry.mesh.quaternion.set(b.quat[0], b.quat[1], b.quat[2], b.quat[3]);
	}
	for (const [id, entry] of meshes) {
		if (!seen.has(id)) {
			entry.mesh.geometry.dispose();
			entry.mesh.material.dispose();
			scene.remove(entry.mesh);
			meshes.delete(id);
		}
	}
}

// ---- player (camera only; position comes from the Jolt character) ----
let playerPos = [0, EYE_HEIGHT, 12];
let yaw = Math.PI;
let pitch = 0;
const keys = new Set();
let jumpQueued = false;

function applyLook(dx, dy) {
	yaw -= dx * 0.0022;
	pitch -= dy * 0.0022;
	const limit = Math.PI / 2 - 0.05;
	pitch = Math.max(-limit, Math.min(limit, pitch));
}

function wishVelocity() {
	let fwd = 0;
	let strafe = 0;
	if (keys.has('KeyW')) fwd += 1;
	if (keys.has('KeyS')) fwd -= 1;
	if (keys.has('KeyD')) strafe += 1;
	if (keys.has('KeyA')) strafe -= 1;
	const speed = keys.has('ShiftLeft') || keys.has('ShiftRight') ? 14 : 8;

	const fx = -Math.sin(yaw), fz = -Math.cos(yaw);
	const rx = Math.cos(yaw), rz = -Math.sin(yaw);
	let wx = fx * fwd + rx * strafe;
	let wz = fz * fwd + rz * strafe;
	const len = Math.hypot(wx, wz);
	if (len > 1e-6) {
		wx = (wx / len) * speed;
		wz = (wz / len) * speed;
	}
	return [wx, wz];
}

function updateCamera() {
	camera.position.set(playerPos[0], playerPos[1] + EYE_HEIGHT, playerPos[2]);
	camera.rotation.set(pitch, yaw, 0);
}

// ---- audio ----
let audioCtx = null;
function ensureAudio() {
	if (!audioCtx) audioCtx = new (window.AudioContext || window.webkitAudioContext)();
	if (audioCtx.state === 'suspended') audioCtx.resume();
	return audioCtx;
}

function tone(freq, dur, type, gain, slideTo) {
	const ctx = ensureAudio();
	const t = ctx.currentTime;
	const osc = ctx.createOscillator();
	const g = ctx.createGain();
	osc.type = type;
	osc.frequency.setValueAtTime(freq, t);
	if (slideTo) osc.frequency.exponentialRampToValueAtTime(Math.max(20, slideTo), t + dur);
	g.gain.setValueAtTime(gain, t);
	g.gain.exponentialRampToValueAtTime(0.0001, t + dur);
	osc.connect(g).connect(ctx.destination);
	osc.start(t);
	osc.stop(t + dur);
}

function noiseBurst(dur, gain, filterFreq) {
	const ctx = ensureAudio();
	const t = ctx.currentTime;
	const buffer = ctx.createBuffer(1, ctx.sampleRate * dur, ctx.sampleRate);
	const data = buffer.getChannelData(0);
	for (let i = 0; i < data.length; i++) data[i] = (Math.random() * 2 - 1) * (1 - i / data.length);
	const src = ctx.createBufferSource();
	src.buffer = buffer;
	const filter = ctx.createBiquadFilter();
	filter.type = 'lowpass';
	filter.frequency.value = filterFreq;
	const g = ctx.createGain();
	g.gain.setValueAtTime(gain, t);
	g.gain.exponentialRampToValueAtTime(0.0001, t + dur);
	src.connect(filter).connect(g).connect(ctx.destination);
	src.start(t);
}

const sfx = {
	shoot() { noiseBurst(0.08, 0.5, 3000); tone(900, 0.06, 'square', 0.08, 300); },
	hit() { noiseBurst(0.1, 0.5, 1200); tone(180, 0.09, 'triangle', 0.2, 80); },
	destroy() { tone(500, 0.22, 'sawtooth', 0.2, 90); noiseBurst(0.16, 0.5, 1800); },
	jump() { tone(250, 0.14, 'sine', 0.12, 500); },
	damage() { tone(120, 0.18, 'sawtooth', 0.2, 60); },
};

// ---- effects ----
const effects = [];

function pop(point, color, size, ttl) {
	const mesh = new THREE.Mesh(
		new THREE.SphereGeometry(size, 12, 8),
		new THREE.MeshBasicMaterial({ color, transparent: true, opacity: 1 })
	);
	mesh.position.copy(point);
	scene.add(mesh);
	effects.push({ kind: 'pop', obj: mesh, life: 0, ttl });
}

function updateEffects(dt) {
	for (let i = effects.length - 1; i >= 0; i--) {
		const e = effects[i];
		e.life += dt;
		const t = e.life / e.ttl;
		if (t >= 1) {
			e.obj.geometry.dispose();
			e.obj.material.dispose();
			scene.remove(e.obj);
			effects.splice(i, 1);
			continue;
		}
		e.obj.scale.setScalar(1 + t * 3);
		e.obj.material.opacity = 1 - t;
	}
}

// ---- projectile impact detection ----
const prevProjectiles = new Map(); // id -> [x,y,z]
function detectProjectileImpacts(bodies) {
	const current = new Map();
	for (const b of bodies) {
		if (b.projectile) current.set(b.id, b.pos);
	}
	for (const [id, pos] of prevProjectiles) {
		if (!current.has(id)) {
			pop(new THREE.Vector3(pos[0], pos[1], pos[2]), COLORS.projectile, 0.06, 0.2);
			sfx.hit();
		}
	}
	prevProjectiles.clear();
	for (const [id, pos] of current) prevProjectiles.set(id, pos);
}

// ---- API ----
async function api(path, opts) {
	const res = await fetch(path, opts);
	if (!res.ok) throw new Error(`${path}: ${res.status}`);
	return res.json();
}

async function refresh() {
	applyState(await api('/api/state'));
}

let lastScore = 0;
function updateHud(s) {
	const bodies = s.bodies || [];
	document.getElementById('score').textContent = s.score ?? 0;
	document.getElementById('targets').textContent = bodies.filter((b) => b.target).length;
	document.getElementById('enemies').textContent = bodies.filter((b) => b.enemy).length;
	document.getElementById('healthfill').style.width = Math.max(0, Math.min(100, s.player?.health ?? 0)) + '%';
	if (s.score > lastScore) sfx.destroy();
	lastScore = s.score;
}

async function shoot() {
	const dir = new THREE.Vector3();
	camera.getWorldDirection(dir);
	const origin = camera.position.clone();
	sfx.shoot();

	// Muzzle flash.
	pop(origin.clone().addScaledVector(dir, 0.7), COLORS.projectile, 0.08, 0.08);

	const resp = await api('/api/shoot', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ origin: [origin.x, origin.y, origin.z], dir: [dir.x, dir.y, dir.z] }),
	});
	applyState(resp.state);
}

// ---- physics stepping (fixed 60 Hz) ----
let accumulator = 0;
let stepPending = false;

function doStep() {
	const [wx, wz] = wishVelocity();
	const jump = jumpQueued;
	jumpQueued = false;
	return api('/api/step', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ frames: 1, move: [wx, wz], jump }),
	}).then(applyState);
}

// ---- main loop ----
let last = performance.now();
let fpsEMA = 60;

function tick(now) {
	requestAnimationFrame(tick);
	const dt = Math.min(0.1, (now - last) / 1000);
	last = now;
	fpsEMA += (1 / Math.max(0.001, dt) - fpsEMA) * 0.08;
	document.getElementById('fps').textContent = Math.round(fpsEMA);

	updateEffects(dt);
	updateCamera();

	if (document.pointerLockElement === renderer.domElement) {
		accumulator += dt;
		while (accumulator >= STEP) {
			accumulator -= STEP;
			if (!stepPending) {
				stepPending = true;
				doStep().catch(() => {}).finally(() => { stepPending = false; });
			}
		}
	}

	renderer.render(scene, camera);
}

function resize() {
	const w = window.innerWidth;
	const h = window.innerHeight;
	renderer.setSize(w, h);
	camera.aspect = w / h;
	camera.updateProjectionMatrix();
}

window.addEventListener('resize', resize);

// ---- input ----
const overlay = document.getElementById('overlay');
overlay.addEventListener('click', () => {
	ensureAudio();
	renderer.domElement.requestPointerLock();
});

document.addEventListener('pointerlockchange', () => {
	const locked = document.pointerLockElement === renderer.domElement;
	overlay.style.display = locked ? 'none' : 'flex';
});

document.addEventListener('mousemove', (e) => {
	if (document.pointerLockElement === renderer.domElement) {
		applyLook(e.movementX, e.movementY);
	}
});

document.addEventListener('mousedown', (e) => {
	if (e.button === 0 && document.pointerLockElement === renderer.domElement) {
		shoot();
	}
});

document.addEventListener('keydown', (e) => {
	if (e.code === 'Space') {
		e.preventDefault();
		if (!keys.has('Space')) {
			jumpQueued = true;
			sfx.jump();
		}
	}
	keys.add(e.code);
});

document.addEventListener('keyup', (e) => {
	keys.delete(e.code);
});

document.getElementById('reset').addEventListener('click', async () => {
	const s = await api('/api/reset', { method: 'POST' });
	lastScore = 0;
	applyState(s);
});

resize();
refresh();
requestAnimationFrame(tick);
