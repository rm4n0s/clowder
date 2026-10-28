// Calls in a conversation (SPEC §7.18). No library and no build step (D13).
//
// The page of a direct conversation has the place of its call, #call-area. The
// server puts the call in it: "Calling Bob…" with Cancel while the user's call
// rings, and the call view when it is active (views/calls.templ). This script
// watches the area. The tab that started the call (it saw the click on a
// button with data-call-start) or answered it (the page has data-answer on the
// call) takes part in it; any other tab shows that the call is in another
// window and keeps the Hang up button.
//
// Taking part is plain WebRTC with the server as the other peer (D63, D64):
// there are no ICE servers. The server always offers (docs/API.md): the script
// opens the signalling socket /calls/<id>/ws, gets the camera and the
// microphone, puts the tracks on the two transceivers of the offer, answers,
// and sends the ICE candidates both ways.
//
// The server patches the area when the call starts, is answered and ends, and
// a patch resets the attributes it sets. So the script keeps a model of the
// call (the microphone, the camera, the marks) and draws it again after every
// change of the area (render), setting an attribute only when it differs.
//
// A test can read what happened: when window.__clowderCallLog is an array,
// every step is pushed to it as a string.
(function () {
  'use strict';

  var area = document.getElementById('call-area');
  if (!area) {
    return;
  }

  var INTENT_MS = 15000;      // a click on a start button counts for this long
  var RECONNECT_MS = 15000;   // time to open the socket again (docs/API.md)
  var RECONNECT_GAP = 1000;   // ms between two tries
  var RING_PERIOD = 6000;     // the ringback repeats every 6 seconds

  var session = null;         // the call this tab takes part in
  var intent = null;          // {video, at}: the start button the user pressed
  var scheduled = false;
  var tickTimer = null;
  var over = {};              // the calls this tab left: a patch may still show them
  var goneTimer = null;       // waits a moment before the end of a call that vanished
  var audio = null;           // the AudioContext of the ringback

  function log(entry) {
    if (Array.isArray(window.__clowderCallLog)) {
      window.__clowderCallLog.push(entry);
    }
  }

  // setAttr sets an attribute only when it differs, so that drawing again does
  // not wake the observer for nothing.
  function setAttr(el, name, value) {
    if (el && el.getAttribute(name) !== value) {
      el.setAttribute(name, value);
    }
  }

  function setText(el, text) {
    if (el && el.textContent !== text) {
      el.textContent = text;
    }
  }

  function setHidden(el, hidden) {
    if (el && el.hidden !== hidden) {
      el.hidden = hidden;
    }
  }

  function panel() {
    return area.querySelector('#call-panel');
  }

  // The ringback: a quiet two-tone ring, repeated while the call of this tab
  // rings. It needs a click on the page first; pressing the start button is one.
  function audioContext() {
    if (!audio) {
      var Ctx = window.AudioContext || window.webkitAudioContext;
      if (!Ctx) {
        return null;
      }
      try {
        audio = new Ctx();
      } catch (err) {
        return null;
      }
    }
    if (audio.state === 'suspended' && audio.resume) {
      audio.resume().catch(function () {});
    }
    return audio;
  }

  function ringbackOnce() {
    var ctx = audioContext();
    if (!ctx || ctx.state !== 'running') {
      return;
    }
    var t = ctx.currentTime + 0.02;
    var master = ctx.createGain();
    master.gain.setValueAtTime(0.0001, t);
    master.gain.exponentialRampToValueAtTime(0.07, t + 0.05);
    master.gain.setValueAtTime(0.07, t + 1.0);
    master.gain.exponentialRampToValueAtTime(0.0001, t + 1.15);
    master.connect(ctx.destination);
    [440, 480].forEach(function (freq) {
      var osc = ctx.createOscillator();
      osc.type = 'sine';
      osc.frequency.setValueAtTime(freq, t);
      osc.connect(master);
      osc.start(t);
      osc.stop(t + 1.2);
    });
  }

  function ringback(s, on) {
    if (on && !s.ringTimer) {
      log('ringback-start');
      ringbackOnce();
      s.ringTimer = setInterval(ringbackOnce, RING_PERIOD);
    } else if (!on && s.ringTimer) {
      clearInterval(s.ringTimer);
      s.ringTimer = null;
      log('ringback-stop');
    }
  }

  // A press on a start button marks this tab as the one that takes part in the
  // call that starts; it also lets the ringback play.
  document.addEventListener('click', function (e) {
    var button = e.target && e.target.closest ? e.target.closest('[data-call-start]') : null;
    if (button) {
      intent = { at: Date.now() };
      audioContext();
    }
  }, true);

  // sync looks at the area after the server changed it.
  function sync() {
    scheduled = false;
    var root = panel();
    if (!root) {
      if (session && !goneTimer) {
        // A patch that replaces the view may leave the area empty for a moment.
        goneTimer = setTimeout(function () {
          goneTimer = null;
          if (session && !panel()) {
            endSession(session, '');
          }
        }, 400);
      }
      return;
    }
    var id = root.getAttribute('data-call');
    if (session && session.id !== id) {
      endSession(session, '');
    }
    if (!session && !over[id] && takesPart(root)) {
      session = newSession(root);
      start(session);
    }
    if (session) {
      update(session, root);
    }
    render();
  }

  // takesPart says whether this tab takes part in the call of the root.
  function takesPart(root) {
    var role = root.getAttribute('data-role');
    if (role === 'callee') {
      return (root.getAttribute('data-answer') || '') !== '' && root.getAttribute('data-state') === 'active';
    }
    if (intent && Date.now() - intent.at < INTENT_MS) {
      intent = null;
      return true;
    }
    return false;
  }

  function newSession(root) {
    return {
      id: root.getAttribute('data-call'),
      role: root.getAttribute('data-role'),
      state: root.getAttribute('data-state'),
      socketPath: root.getAttribute('data-socket'),
      hangupPath: root.getAttribute('data-hangup'),
      cancelPath: root.getAttribute('data-cancel'),
      sendVideo: root.getAttribute('data-video') === 'true',
      pc: null,
      ws: null,
      audioTrack: null,
      videoTrack: null,
      screenTrack: null,
      audioSender: null,
      videoSender: null,
      remote: new MediaStream(),
      mic: false,
      cam: false,
      sharing: false,
      peerMic: undefined,
      peerCam: undefined,
      connected: false,
      ended: false,
      status: 'Connecting…',
      pendingCandidates: [],
      hasRemote: false,
      reconnectUntil: 0,
      reconnectTimer: null,
      ringTimer: null,
      base: null,
      minimized: false,
      needsPlay: false,
      cameraDevice: '',
      micDevice: '',
      devicesKey: ''
    };
  }

  // update takes the changes of the server's state into the session.
  function update(s, root) {
    var state = root.getAttribute('data-state');
    if (state !== s.state) {
      s.state = state;
      log('state-' + state);
    }
    if (state === 'active' && s.base === null) {
      var elapsed = parseInt(root.getAttribute('data-elapsed'), 10) || 0;
      s.base = elapsed * 1000 - performance.now();
    }
    ringback(s, s.role === 'caller' && s.state === 'ringing');
  }

  // The media.

  function describe(err) {
    var name = err && err.name;
    if (name === 'NotAllowedError' || name === 'SecurityError') {
      return 'The browser did not allow the camera or the microphone.';
    }
    if (name === 'NotFoundError') {
      return 'No camera or microphone was found.';
    }
    return 'The camera or the microphone could not be used.';
  }

  async function acquire(s) {
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      s.status = 'Calls need a secure page (https).';
      return;
    }
    var stream = null;
    var tries = s.sendVideo ? [{ audio: true, video: true }, { audio: true }, { video: true }] : [{ audio: true }];
    for (var i = 0; i < tries.length && !stream; i++) {
      try {
        stream = await navigator.mediaDevices.getUserMedia(tries[i]);
      } catch (err) {
        s.status = describe(err);
      }
    }
    if (!stream) {
      return;
    }
    s.audioTrack = stream.getAudioTracks()[0] || null;
    s.videoTrack = stream.getVideoTracks()[0] || null;
    s.mic = s.audioTrack !== null;
    s.cam = s.videoTrack !== null;
    if (s.mic && (!s.sendVideo || s.cam)) {
      s.status = 'Connecting…';
    }
    log('media mic=' + s.mic + ' cam=' + s.cam);
  }

  async function start(s) {
    try {
      await acquire(s);
    } catch (err) {
      s.status = describe(err);
    }
    if (s.ended) {
      stopTracks(s);
      return;
    }
    refreshDevices(s);
    connect(s);
    render();
  }

  function stopTracks(s) {
    [s.audioTrack, s.videoTrack, s.screenTrack].forEach(function (track) {
      if (track) {
        track.onended = null;
        track.stop();
      }
    });
    s.audioTrack = s.videoTrack = s.screenTrack = null;
  }

  // The signalling socket.

  function send(s, message) {
    if (s.ws && s.ws.readyState === WebSocket.OPEN) {
      s.ws.send(JSON.stringify(message));
    }
  }

  function connect(s) {
    var scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws;
    try {
      ws = new WebSocket(scheme + '//' + location.host + s.socketPath);
    } catch (err) {
      fail(s, 'The call could not connect.');
      return;
    }
    s.ws = ws;
    ws.onopen = function () {
      log('socket-open');
      s.reconnectUntil = 0;
    };
    ws.onmessage = function (e) {
      var msg;
      try {
        msg = JSON.parse(e.data);
      } catch (err) {
        return;
      }
      onMessage(s, msg);
    };
    ws.onclose = function (e) {
      if (s.ws === ws) {
        onClose(s, e);
      }
    };
  }

  function onMessage(s, msg) {
    switch (msg.type) {
      case 'offer':
        onOffer(s, msg.sdp);
        break;
      case 'candidate':
        onCandidate(s, msg);
        break;
      case 'peer-state':
        s.peerMic = msg.microphone;
        s.peerCam = msg.camera;
        render();
        break;
      case 'ended':
        log('ended ' + msg.reason);
        endSession(s, 'The call ended.');
        break;
      case 'error':
        log('error ' + msg.code);
        if (msg.code === 'negotiation_failed') {
          s.status = 'The call could not connect.';
          render();
        }
        break;
    }
  }

  function onClose(s, e) {
    log('socket-close ' + e.code);
    if (s.ended) {
      return;
    }
    if (e.code === 4001) {
      endSession(s, 'Your session ended.');
      location.href = '/login';
      return;
    }
    if (e.code === 4002) {
      endSession(s, 'Your account changed. Reload the page.');
      return;
    }
    if (e.code === 4003) {
      endSession(s, 'This call moved to another window.');
      return;
    }
    if (e.code === 1000) {
      return;
    }
    reconnect(s);
  }

  // reconnect opens the socket again, within the time the server allows. The
  // server then sends a new offer, which makes a new connection.
  function reconnect(s) {
    var now = Date.now();
    if (!s.reconnectUntil) {
      s.reconnectUntil = now + RECONNECT_MS;
      closePeer(s);
      s.connected = false;
      s.status = 'Reconnecting…';
      render();
    }
    if (now >= s.reconnectUntil) {
      fail(s, 'The connection was lost.');
      return;
    }
    s.reconnectTimer = setTimeout(function () {
      s.reconnectTimer = null;
      if (!s.ended) {
        connect(s);
      }
    }, RECONNECT_GAP);
  }

  function fail(s, message) {
    log('fail ' + message);
    endSession(s, message);
  }

  // The peer connection.

  function closePeer(s) {
    if (s.pc) {
      s.pc.onicecandidate = s.pc.ontrack = s.pc.onconnectionstatechange = null;
      try {
        s.pc.close();
      } catch (err) {
        // Already closed.
      }
    }
    s.pc = null;
    s.audioSender = s.videoSender = null;
    s.hasRemote = false;
    s.pendingCandidates = [];
    s.remote = new MediaStream();
  }

  function makePeer(s) {
    var pc = new RTCPeerConnection({ iceServers: [] });
    pc.onicecandidate = function (e) {
      if (e.candidate) {
        send(s, {
          type: 'candidate',
          candidate: e.candidate.candidate,
          sdpMid: e.candidate.sdpMid,
          sdpMLineIndex: e.candidate.sdpMLineIndex
        });
      }
    };
    pc.ontrack = function (e) {
      s.remote.addTrack(e.track);
      e.track.onmute = e.track.onunmute = render;
      render();
    };
    pc.onconnectionstatechange = function () {
      log('pc-' + pc.connectionState);
      if (pc.connectionState === 'connected') {
        s.connected = true;
        s.status = '';
      } else if (pc.connectionState === 'disconnected') {
        s.status = 'Reconnecting…';
      } else if (pc.connectionState === 'failed') {
        s.connected = false;
        s.status = 'The connection was lost.';
      }
      render();
    };
    return pc;
  }

  async function onOffer(s, sdp) {
    try {
      if (!s.pc) {
        s.pc = makePeer(s);
      }
      var pc = s.pc;
      await pc.setRemoteDescription({ type: 'offer', sdp: sdp });
      s.hasRemote = true;
      for (const t of pc.getTransceivers()) {
        var kind = t.receiver && t.receiver.track ? t.receiver.track.kind : '';
        if (kind === 'audio') {
          s.audioSender = t.sender;
          t.direction = 'sendrecv';
          await t.sender.replaceTrack(s.audioTrack);
        } else if (kind === 'video') {
          s.videoSender = t.sender;
          t.direction = 'sendrecv';
          await t.sender.replaceTrack(s.screenTrack || s.videoTrack);
        }
      }
      var answer = await pc.createAnswer();
      await pc.setLocalDescription(answer);
      send(s, { type: 'answer', sdp: pc.localDescription.sdp });
      var queued = s.pendingCandidates;
      s.pendingCandidates = [];
      queued.forEach(function (c) {
        addCandidate(s, c);
      });
      sendState(s);
      log('answered');
    } catch (err) {
      log('offer-failed ' + (err && err.name));
      s.status = 'The call could not connect.';
      render();
    }
  }

  function onCandidate(s, msg) {
    var c = { candidate: msg.candidate, sdpMid: msg.sdpMid, sdpMLineIndex: msg.sdpMLineIndex };
    if (!s.pc || !s.hasRemote) {
      s.pendingCandidates.push(c);
      return;
    }
    addCandidate(s, c);
  }

  function addCandidate(s, c) {
    s.pc.addIceCandidate(c).catch(function (err) {
      log('candidate-failed ' + (err && err.name));
    });
  }

  // What the other side sees of the user: the microphone, and whether any video
  // (the camera or the shared screen) is sent.
  function outgoingVideo(s) {
    return s.sharing || s.cam;
  }

  function sendState(s) {
    send(s, { type: 'state', microphone: s.mic, camera: outgoingVideo(s) });
  }

  // The controls.

  async function toggleMic(s) {
    if (!s.audioTrack) {
      try {
        var stream = await navigator.mediaDevices.getUserMedia({ audio: deviceConstraint(s.micDevice) });
        s.audioTrack = stream.getAudioTracks()[0];
        if (s.audioSender) {
          await s.audioSender.replaceTrack(s.audioTrack);
        }
        s.mic = true;
      } catch (err) {
        s.status = describe(err);
      }
    } else {
      s.audioTrack.enabled = !s.audioTrack.enabled;
      s.mic = s.audioTrack.enabled;
    }
    sendState(s);
    render();
  }

  function deviceConstraint(id) {
    return id ? { deviceId: { exact: id } } : true;
  }

  async function toggleCamera(s) {
    try {
      if (s.cam) {
        if (s.videoTrack) {
          s.videoTrack.stop();
          s.videoTrack = null;
        }
        s.cam = false;
        if (s.videoSender && !s.sharing) {
          await s.videoSender.replaceTrack(null);
        }
      } else {
        var stream = await navigator.mediaDevices.getUserMedia({ video: deviceConstraint(s.cameraDevice) });
        s.videoTrack = stream.getVideoTracks()[0];
        s.cam = true;
        if (s.videoSender && !s.sharing) {
          await s.videoSender.replaceTrack(s.videoTrack);
        }
        refreshDevices(s);
      }
    } catch (err) {
      s.status = describe(err);
    }
    sendState(s);
    render();
  }

  async function toggleScreen(s) {
    if (s.sharing) {
      await stopSharing(s);
      return;
    }
    try {
      var stream = await navigator.mediaDevices.getDisplayMedia({ video: true, audio: false });
      var track = stream.getVideoTracks()[0];
      s.screenTrack = track;
      track.onended = function () {
        if (s.screenTrack === track) {
          stopSharing(s);
        }
      };
      s.sharing = true;
      if (s.videoSender) {
        await s.videoSender.replaceTrack(track);
      }
      log('sharing');
    } catch (err) {
      log('share-failed ' + (err && err.name));
    }
    sendState(s);
    render();
  }

  // stopSharing goes back to the camera, or to no video when it is off.
  async function stopSharing(s) {
    var track = s.screenTrack;
    s.screenTrack = null;
    s.sharing = false;
    if (track) {
      track.onended = null;
      track.stop();
    }
    try {
      if (s.videoSender) {
        await s.videoSender.replaceTrack(s.cam ? s.videoTrack : null);
      }
    } catch (err) {
      log('replace-failed ' + (err && err.name));
    }
    log('sharing-stopped');
    sendState(s);
    render();
  }

  async function chooseDevice(s, kind, id) {
    try {
      if (kind === 'audioinput') {
        s.micDevice = id;
        var a = await navigator.mediaDevices.getUserMedia({ audio: deviceConstraint(id) });
        var micTrack = a.getAudioTracks()[0];
        micTrack.enabled = s.mic || !s.audioTrack;
        if (s.audioTrack) {
          s.audioTrack.stop();
        }
        s.audioTrack = micTrack;
        s.mic = micTrack.enabled;
        if (s.audioSender) {
          await s.audioSender.replaceTrack(micTrack);
        }
      } else if (kind === 'videoinput') {
        s.cameraDevice = id;
        if (s.cam) {
          var v = await navigator.mediaDevices.getUserMedia({ video: deviceConstraint(id) });
          var camTrack = v.getVideoTracks()[0];
          if (s.videoTrack) {
            s.videoTrack.stop();
          }
          s.videoTrack = camTrack;
          if (s.videoSender && !s.sharing) {
            await s.videoSender.replaceTrack(camTrack);
          }
        }
      }
    } catch (err) {
      s.status = describe(err);
    }
    sendState(s);
    refreshDevices(s);
    render();
  }

  // refreshDevices reads the devices the browser knows; the labels are there
  // once the user allowed the camera or the microphone.
  function refreshDevices(s) {
    if (!navigator.mediaDevices || !navigator.mediaDevices.enumerateDevices) {
      return;
    }
    navigator.mediaDevices.enumerateDevices().then(function (list) {
      s.devices = list.filter(function (d) {
        return d.kind === 'audioinput' || d.kind === 'videoinput';
      });
      render();
    }).catch(function () {});
  }

  area.addEventListener('click', function (e) {
    var button = e.target && e.target.closest ? e.target.closest('[data-call-control]') : null;
    if (!button || !session) {
      return;
    }
    var s = session;
    switch (button.getAttribute('data-call-control')) {
      case 'mic':
        toggleMic(s);
        break;
      case 'camera':
        toggleCamera(s);
        break;
      case 'screen':
        toggleScreen(s);
        break;
      case 'minimize':
        s.minimized = !s.minimized;
        render();
        break;
      case 'resume':
        s.needsPlay = false;
        playRemote(s, true);
        render();
        break;
    }
  });

  area.addEventListener('change', function (e) {
    var select = e.target;
    if (session && select && select.matches && select.matches('select[data-call-device]')) {
      chooseDevice(session, select.getAttribute('data-call-device'), select.value);
    }
  });

  // Drawing.

  function mmss(ms) {
    var total = Math.max(0, Math.floor(ms / 1000));
    var h = Math.floor(total / 3600);
    var m = Math.floor(total % 3600 / 60);
    var sec = total % 60;
    function two(n) {
      return n < 10 ? '0' + n : String(n);
    }
    return h > 0 ? h + ':' + two(m) + ':' + two(sec) : two(m) + ':' + two(sec);
  }

  function labelled(button, pressed, onText, offText) {
    if (!button) {
      return;
    }
    setAttr(button, 'aria-pressed', pressed ? 'true' : 'false');
    var text = pressed ? onText : offText;
    setAttr(button, 'aria-label', text);
    setAttr(button, 'title', text);
  }

  // playRemote starts the video element that holds the other side. A browser
  // may refuse to play sound before the user touched the page; the button
  // "Tap to play the call" then asks for the touch.
  function playRemote(s, byUser) {
    var video = area.querySelector('#call-remote');
    if (!video) {
      return;
    }
    var p = video.play();
    if (p && p.catch) {
      p.catch(function (err) {
        if (err && err.name === 'NotAllowedError' && !byUser) {
          log('autoplay-refused');
          s.needsPlay = true;
          render();
        }
      });
    }
  }

  function hasPeerVideo(s) {
    if (s.peerCam === false) {
      return false;
    }
    var tracks = s.remote.getVideoTracks();
    return tracks.length > 0 && !tracks[0].muted;
  }

  function render() {
    var root = panel();
    if (!root) {
      return;
    }
    var s = session;
    setAttr(root, 'data-owner', s ? '1' : '0');
    if (!s) {
      return;
    }
    setAttr(root, 'data-connected', s.connected ? '1' : '0');
    setAttr(root, 'data-minimized', s.minimized ? '1' : '0');
    setAttr(root, 'data-sharing', s.sharing ? '1' : '0');
    setAttr(root, 'data-peer-video', hasPeerVideo(s) ? '1' : '0');
    setAttr(root, 'data-local-video', outgoingVideo(s) ? '1' : '0');
    setText(root.querySelector('.call-status'), s.status);
    setHidden(root.querySelector('.call-status'), s.status === '');

    labelled(root.querySelector('[data-call-control="mic"]'), s.mic, 'Mute the microphone', 'Unmute the microphone');
    labelled(root.querySelector('[data-call-control="camera"]'), s.cam, 'Turn the camera off', 'Turn the camera on');
    labelled(root.querySelector('[data-call-control="screen"]'), s.sharing, 'Stop sharing your screen', 'Share your screen');
    labelled(root.querySelector('[data-call-control="minimize"]'), s.minimized, 'Show the call', 'Show the conversation');
    setHidden(root.querySelector('[data-call-control="resume"]'), !s.needsPlay);
    // A browser that cannot share a screen (most phones) has no button for it.
    setHidden(root.querySelector('[data-call-control="screen"]'),
      !(navigator.mediaDevices && navigator.mediaDevices.getDisplayMedia));

    var micMark = root.querySelector('.call-mark-mic');
    var camMark = root.querySelector('.call-mark-cam');
    setHidden(micMark, s.peerMic !== false);
    setHidden(camMark, s.peerCam !== false);

    var remote = root.querySelector('#call-remote');
    if (remote && remote.srcObject !== s.remote) {
      remote.srcObject = s.remote;
      playRemote(s, false);
    }
    var local = root.querySelector('#call-local');
    if (local) {
      var shown = s.screenTrack || (s.cam ? s.videoTrack : null);
      var localStream = local.srcObject;
      var wanted = shown ? shown.id : '';
      var have = localStream && localStream.getVideoTracks()[0] ? localStream.getVideoTracks()[0].id : '';
      if (wanted !== have) {
        local.srcObject = shown ? new MediaStream([shown]) : null;
        if (shown) {
          local.play().catch(function () {});
        }
      }
    }
    renderDevices(s, root);
    renderDuration(s, root);
  }

  function renderDuration(s, root) {
    var el = root.querySelector('[data-call-duration]');
    if (el && s.base !== null) {
      setText(el, mmss(performance.now() + s.base));
    }
  }

  // renderDevices fills the two selects of the devices menu with what the
  // browser knows, and marks the ones in use.
  function renderDevices(s, root) {
    var devices = s.devices || [];
    var key = devices.map(function (d) {
      return d.kind + ':' + d.deviceId + ':' + d.label;
    }).join('|') + '#' + (s.audioTrack ? s.audioTrack.getSettings().deviceId : '') +
      '#' + (s.videoTrack ? s.videoTrack.getSettings().deviceId : '');
    root.querySelectorAll('select[data-call-device]').forEach(function (select) {
      var kind = select.getAttribute('data-call-device');
      var list = devices.filter(function (d) {
        return d.kind === kind;
      });
      if (select.options.length === list.length && select.getAttribute('data-key') === key) {
        return;
      }
      while (select.firstChild) {
        select.removeChild(select.firstChild);
      }
      var track = kind === 'audioinput' ? s.audioTrack : s.videoTrack;
      var current = track && track.getSettings ? track.getSettings().deviceId : '';
      list.forEach(function (d, i) {
        var option = document.createElement('option');
        option.value = d.deviceId;
        option.textContent = d.label || (kind === 'audioinput' ? 'Microphone ' : 'Camera ') + (i + 1);
        option.selected = d.deviceId === current;
        select.appendChild(option);
      });
      select.setAttribute('data-key', key);
    });
  }

  // endSession leaves the call: the socket and the peer connection close, the
  // camera and the microphone are released, and the ringback stops. message,
  // when there is one, is what the call view says until the server takes it away.
  function endSession(s, message) {
    if (s.ended) {
      return;
    }
    s.ended = true;
    over[s.id] = true;
    ringback(s, false);
    if (s.reconnectTimer) {
      clearTimeout(s.reconnectTimer);
    }
    if (s.ws) {
      var ws = s.ws;
      s.ws = null;
      ws.onclose = ws.onmessage = null;
      try {
        ws.close(1000);
      } catch (err) {
        // Already closed.
      }
    }
    closePeer(s);
    stopTracks(s);
    var root = panel();
    if (root && message) {
      setText(root.querySelector('.call-status') || root.querySelector('.call-calling-line'), message);
      setHidden(root.querySelector('.call-status'), false);
      setAttr(root, 'data-connected', '0');
      setAttr(root, 'data-ended', '1');
    }
    var video = area.querySelector('#call-remote');
    if (video) {
      video.srcObject = null;
    }
    var local = area.querySelector('#call-local');
    if (local) {
      local.srcObject = null;
    }
    if (session === s) {
      session = null;
    }
    log('session-ended');
  }

  // Leaving the page leaves the call, after the browser's own confirmation.
  window.addEventListener('beforeunload', function (e) {
    if (session && !session.ended && session.state === 'active') {
      e.preventDefault();
      e.returnValue = '';
      return '';
    }
    return undefined;
  });

  window.addEventListener('pagehide', function () {
    var s = session;
    if (!s || s.ended) {
      return;
    }
    var path = '';
    if (s.state === 'active') {
      path = s.hangupPath;
    } else if (s.role === 'caller') {
      path = s.cancelPath;
    }
    if (path && navigator.sendBeacon) {
      try {
        navigator.sendBeacon(path, new Blob([''], { type: 'application/x-www-form-urlencoded' }));
      } catch (err) {
        // The server ends the call when the socket stays closed.
      }
    }
  });

  if (navigator.mediaDevices && navigator.mediaDevices.addEventListener) {
    navigator.mediaDevices.addEventListener('devicechange', function () {
      if (session) {
        refreshDevices(session);
      }
    });
  }

  // The duration moves once a second.
  tickTimer = setInterval(function () {
    var root = panel();
    if (root && session) {
      renderDuration(session, root);
    }
  }, 1000);

  function schedule() {
    if (!scheduled) {
      scheduled = true;
      Promise.resolve().then(sync);
    }
  }

  new MutationObserver(schedule).observe(area, { childList: true, subtree: true, attributes: true });
  schedule();
})();
