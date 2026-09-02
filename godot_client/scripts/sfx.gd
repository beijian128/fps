extends Node
## 程序化音效：全部运行时生成（对应浏览器版 Web Audio 合成），无外部音频文件。

const RATE := 44100

enum Wave { SINE, SAW, SQUARE, TRI }

var _players := {}

func _ready() -> void:
	var defs := {
		"shoot": _noise_burst(0.08, 0.5, 3000.0),
		"hit": _noise_burst(0.10, 0.5, 1200.0),
		"destroy": _tone(500.0, 0.22, Wave.SAW, 0.2, 90.0),
		"jump": _tone(250.0, 0.14, Wave.SINE, 0.12, 500.0),
		"damage": _tone(120.0, 0.18, Wave.SAW, 0.2, 60.0),
		"pickup": _tone(880.0, 0.16, Wave.SINE, 0.22, 1520.0),
	}
	for key in defs:
		var p := AudioStreamPlayer.new()
		p.stream = defs[key]
		p.volume_db = -6.0
		add_child(p)
		_players[key] = p

func play(sound_name: String) -> void:
	var p: AudioStreamPlayer = _players.get(sound_name)
	if p != null:
		p.play()

## 带指数下滑的波形音（对应 Web Audio 的 oscillator + exponentialRamp）。
func _tone(freq: float, dur: float, wave: Wave, gain: float, slide_to: float = 0.0) -> AudioStreamWAV:
	var n := maxi(1, int(RATE * dur))
	var data := PackedByteArray()
	data.resize(n * 2)
	var phase := 0.0
	for i in n:
		var t := float(i) / float(n)
		var f := freq
		if slide_to > 0.0:
			f = freq * pow(slide_to / freq, t)
		phase += f / float(RATE)
		var s := 0.0
		match wave:
			Wave.SINE:
				s = sin(TAU * phase)
			Wave.SAW:
				s = 2.0 * fposmod(phase, 1.0) - 1.0
			Wave.SQUARE:
				s = 1.0 if fposmod(phase, 1.0) < 0.5 else -1.0
			Wave.TRI:
				s = 4.0 * abs(fposmod(phase, 1.0) - 0.5) - 1.0
		var env := exp(-5.0 * t)
		var v := clampf(s * env * gain, -1.0, 1.0)
		data.encode_s16(i * 2, int(v * 32767.0))
	return _to_wav(data)

## 低通噪声爆音（对应 Web Audio 的 noise + lowpass filter）。
func _noise_burst(dur: float, gain: float, cutoff: float) -> AudioStreamWAV:
	var n := maxi(1, int(RATE * dur))
	var data := PackedByteArray()
	data.resize(n * 2)
	var rng := RandomNumberGenerator.new()
	var alpha := 1.0 - exp(-TAU * cutoff / float(RATE))
	var y := 0.0
	for i in n:
		y += alpha * (rng.randf_range(-1.0, 1.0) - y)
		var t := float(i) / float(n)
		var v := clampf(y * (1.0 - t) * gain, -1.0, 1.0)
		data.encode_s16(i * 2, int(v * 32767.0))
	return _to_wav(data)

func _to_wav(data: PackedByteArray) -> AudioStreamWAV:
	var w := AudioStreamWAV.new()
	w.format = AudioStreamWAV.FORMAT_16_BITS
	w.mix_rate = RATE
	w.stereo = false
	w.data = data
	return w
