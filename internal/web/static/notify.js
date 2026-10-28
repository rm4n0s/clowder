// Sounds and browser notifications (SPEC §7.15). No library and no build step
// (D13). The server decides whether an event rings for the user, from their
// preferences, and tells the page by appending an element to #notify-events:
//
//   <div class="notify-event" data-id="n12" data-kind="notification.created"
//        data-notice="Bob mentioned you in #general" data-sound="purr"
//        data-browser="1" data-place="room:3" data-url="/posts/7#post-9"></div>
//
// This script obeys it, with the rules that only the browser can know:
//
//   - the sound is made with the Web Audio API (no audio files), at most one
//     every 2 seconds, and nothing plays before the person has touched the
//     page (the browser forbids it); nothing is queued;
//   - the browser notification shows only when no Clowder tab has the focus;
//   - neither happens for the room or conversation that is open in the
//     focused tab;
//   - every tab of the person receives the event, so the tabs elect one of
//     them through BroadcastChannel("clowder-notify"): the event rings once.
//
// The same script rings for a call (SPEC §7.18). The user's stream puts the
// dialog of the call that rings in #call-ringing; this script opens it as a
// dialog, plays the ringtone *Purr ring* (Web Audio, repeated until the dialog
// goes away) in one tab of the user when the server says so (data-ring), and
// shows the browser notification "Bob is calling you" when no Clowder tab has
// the focus (data-browser). The ringtone does not depend on the user's
// notification sound. If the browser refuses sound before the page was touched,
// the dialog still shows and the ringtone starts at the first touch.
//
// A test can read what the script decided: when window.__clowderNotifyLog is
// an array, every decision of the tab that rings is pushed to it as
// {kind, sound, notified, played}.
(function () {
  'use strict';

  var SOUND_GAP = 2000;      // ms between two sounds
  var ELECTION_WAIT = 120;   // ms the tabs have to announce themselves
  var CHANNEL = 'clowder-notify';

  var host = document.getElementById('notify-events');
  if (!host) {
    return;
  }

  var tabID = Math.random().toString(36).slice(2) + Date.now().toString(36);
  var touched = false;       // the person has touched the page
  var lastSound = 0;         // when the last sound played
  var audio = null;          // the AudioContext, made after the first touch
  var seen = {};             // the events this tab has taken up
  var claims = {};           // event id -> the announcements of the tabs
  var channel = null;
  var ringing = null;        // the call that rings in this tab: {id, tick, timer}

  try {
    if ('BroadcastChannel' in window) {
      channel = new BroadcastChannel(CHANNEL);
      channel.onmessage = function (e) {
        var m = e.data;
        if (m && m.type === 'claim' && typeof m.id === 'string') {
          (claims[m.id] = claims[m.id] || []).push(m);
        }
      };
    }
  } catch (err) {
    channel = null;
  }

  // The first touch of the page lets sound play.
  ['pointerdown', 'keydown', 'touchstart'].forEach(function (name) {
    window.addEventListener(name, function () {
      touched = true;
      context();
      if (ringing && ringing.tick) {
        ringing.tick();
      }
    }, { capture: true, passive: true });
  });

  function context() {
    if (!touched) {
      return null;
    }
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
      audio.resume();
    }
    return audio;
  }

  // The four sounds, each under a second and gentle.

  // note makes one oscillator with an envelope: it starts at `start`, rises in
  // `attack` seconds to `peak` and falls away exponentially in `decay` seconds.
  function note(ctx, out, type, start, attack, decay, peak, freq) {
    var osc = ctx.createOscillator();
    var gain = ctx.createGain();
    osc.type = type;
    if (typeof freq === 'number') {
      osc.frequency.setValueAtTime(freq, start);
    }
    gain.gain.setValueAtTime(0.0001, start);
    gain.gain.exponentialRampToValueAtTime(peak, start + attack);
    gain.gain.exponentialRampToValueAtTime(0.0001, start + attack + decay);
    osc.connect(gain);
    gain.connect(out);
    osc.start(start);
    osc.stop(start + attack + decay + 0.05);
    return { osc: osc, gain: gain };
  }

  var sounds = {
    // A low, warm rumble with a soft tremolo.
    purr: function (ctx, out, t) {
      var n = note(ctx, out, 'triangle', t, 0.08, 0.7, 0.35, 92);
      var tremolo = ctx.createOscillator();
      var depth = ctx.createGain();
      tremolo.frequency.setValueAtTime(24, t);
      depth.gain.setValueAtTime(0.12, t);
      tremolo.connect(depth);
      depth.connect(n.gain.gain);
      tremolo.start(t);
      tremolo.stop(t + 0.85);
    },
    // Two quick rising notes.
    chirp: function (ctx, out, t) {
      var a = note(ctx, out, 'sine', t, 0.01, 0.12, 0.3, 880);
      a.osc.frequency.exponentialRampToValueAtTime(1040, t + 0.1);
      var b = note(ctx, out, 'sine', t + 0.14, 0.01, 0.16, 0.3, 1175);
      b.osc.frequency.exponentialRampToValueAtTime(1480, t + 0.27);
    },
    // One soft bell.
    bell: function (ctx, out, t) {
      note(ctx, out, 'sine', t, 0.005, 0.85, 0.32, 784);
    },
    // A sweep up and then down, with a little vibrato.
    meow: function (ctx, out, t) {
      var n = note(ctx, out, 'sine', t, 0.06, 0.55, 0.32, 520);
      n.osc.frequency.exponentialRampToValueAtTime(900, t + 0.22);
      n.osc.frequency.exponentialRampToValueAtTime(430, t + 0.62);
      var vibrato = ctx.createOscillator();
      var depth = ctx.createGain();
      vibrato.frequency.setValueAtTime(6, t);
      depth.gain.setValueAtTime(14, t);
      vibrato.connect(depth);
      depth.connect(n.osc.frequency);
      vibrato.start(t);
      vibrato.stop(t + 0.7);
    }
  };

  // play plays the named sound now. It returns true when it was started.
  function play(name) {
    var make = sounds[name];
    var ctx = context();
    if (!make || !ctx) {
      return false;
    }
    try {
      var master = ctx.createGain();
      master.gain.setValueAtTime(0.5, ctx.currentTime);
      master.connect(ctx.destination);
      make(ctx, master, ctx.currentTime + 0.02);
      return true;
    } catch (err) {
      return false;
    }
  }

  // The Play buttons of the settings.
  document.addEventListener('click', function (e) {
    var button = e.target && e.target.closest ? e.target.closest('[data-play-sound]') : null;
    if (button) {
      touched = true;
      play(button.getAttribute('data-play-sound'));
    }
  });

  // Turning on the browser notifications asks for the permission; when the
  // person refuses, the box is unchecked again.
  document.addEventListener('change', function (e) {
    var box = e.target;
    if (!box || box.id !== 'notify-browser' || !box.checked) {
      return;
    }
    var hint = document.getElementById('notify-browser-hint');
    function say(text) {
      if (hint) {
        hint.textContent = text;
      }
    }
    if (!('Notification' in window)) {
      box.checked = false;
      say('This browser cannot show notifications.');
      return;
    }
    if (Notification.permission === 'granted') {
      return;
    }
    if (Notification.permission === 'denied') {
      box.checked = false;
      say('Notifications are blocked for this site in your browser settings.');
      return;
    }
    Notification.requestPermission().then(function (result) {
      if (result !== 'granted') {
        box.checked = false;
        say('Your browser did not give the permission.');
      }
    });
  });

  function currentPlace() {
    var el = document.getElementById('notify-place');
    return el ? el.getAttribute('data-place') : '';
  }

  function log(entry) {
    if (Array.isArray(window.__clowderNotifyLog)) {
      window.__clowderNotifyLog.push(entry);
    }
  }

  // take handles one element the server appended. The tabs announce
  // themselves, and the one that wins rings.
  function take(el) {
    var id = el.getAttribute('data-id') || el.id;
    var event = {
      id: id,
      kind: el.getAttribute('data-kind') || '',
      text: el.getAttribute('data-notice') || '',
      sound: el.getAttribute('data-sound') || '',
      browser: el.hasAttribute('data-browser'),
      place: el.getAttribute('data-place') || '',
      url: el.getAttribute('data-url') || ''
    };
    el.remove();
    if (!id || seen[id]) {
      return;
    }
    seen[id] = true;
    var focused = document.hasFocus();
    var mine = {
      type: 'claim', id: id, tab: tabID,
      focused: focused,
      // The place open in the focused tab is not rung for, by any tab.
      suppress: focused && event.place !== '' && event.place === currentPlace(),
      touched: touched
    };
    (claims[id] = claims[id] || []).push(mine);
    if (channel) {
      channel.postMessage(mine);
    }
    setTimeout(function () { decide(event); }, channel ? ELECTION_WAIT : 0);
  }

  function decide(event) {
    var all = claims[event.id] || [];
    delete claims[event.id];
    if (all.some(function (c) { return c.suppress; })) {
      return;
    }
    // A tab the person has touched can play; among those, the lowest id wins.
    all.sort(function (a, b) {
      if (a.touched !== b.touched) {
        return a.touched ? -1 : 1;
      }
      return a.tab < b.tab ? -1 : a.tab > b.tab ? 1 : 0;
    });
    if (!all.length || all[0].tab !== tabID) {
      return;
    }
    var anyFocus = all.some(function (c) { return c.focused; });
    var now = Date.now();
    var played = false;
    if (event.sound && now - lastSound >= SOUND_GAP) {
      played = play(event.sound);
      if (played) {
        lastSound = now;
      }
    }
    var notified = false;
    if (event.browser && !anyFocus) {
      notified = show(event);
    }
    log({ kind: event.kind, sound: event.sound, notified: notified, played: played });
  }

  // show shows the notification of the browser, if the person allowed it. It
  // returns true when the decision was to show one.
  function show(event) {
    if (!('Notification' in window) || Notification.permission !== 'granted') {
      return false;
    }
    try {
      var n = new Notification(event.text, { tag: event.id });
      n.onclick = function () {
        window.focus();
        if (event.url) {
          window.location.href = event.url;
        }
        n.close();
      };
      return true;
    } catch (err) {
      return false;
    }
  }

  // The ringing dialog of a call (SPEC §7.18).

  var RING_PERIOD = 4000;    // 1.5 s of ringtone and 2.5 s of quiet
  var RING_LIMIT = 50000;    // the server stops the call after 45 s

  // purrRing is the ringtone *Purr ring*: a warm low tone with a purr-like
  // flutter, 1.5 seconds long.
  function purrRing(ctx, out, t) {
    var len = 1.5;
    var body = ctx.createGain();
    body.gain.setValueAtTime(0.0001, t);
    body.gain.exponentialRampToValueAtTime(0.5, t + 0.12);
    body.gain.setValueAtTime(0.5, t + len - 0.35);
    body.gain.exponentialRampToValueAtTime(0.0001, t + len);
    body.connect(out);
    // The flutter: a 26 Hz tremolo on a carrier gain.
    var carrier = ctx.createGain();
    carrier.gain.setValueAtTime(0.55, t);
    var lfo = ctx.createOscillator();
    var depth = ctx.createGain();
    lfo.frequency.setValueAtTime(26, t);
    depth.gain.setValueAtTime(0.4, t);
    lfo.connect(depth);
    depth.connect(carrier.gain);
    carrier.connect(body);
    // The tone: a low triangle with a sine a fifth above it, gliding a little.
    var low = ctx.createOscillator();
    low.type = 'triangle';
    low.frequency.setValueAtTime(130, t);
    low.frequency.linearRampToValueAtTime(150, t + len * 0.5);
    low.frequency.linearRampToValueAtTime(124, t + len);
    var fifth = ctx.createOscillator();
    var fifthGain = ctx.createGain();
    fifth.type = 'sine';
    fifth.frequency.setValueAtTime(195, t);
    fifth.frequency.linearRampToValueAtTime(225, t + len * 0.5);
    fifth.frequency.linearRampToValueAtTime(186, t + len);
    fifthGain.gain.setValueAtTime(0.35, t);
    low.connect(carrier);
    fifth.connect(fifthGain);
    fifthGain.connect(carrier);
    [lfo, low, fifth].forEach(function (osc) {
      osc.start(t);
      osc.stop(t + len + 0.05);
    });
  }

  // ringContext is the AudioContext for the ringtone. It is made even before
  // the page was touched: the browser may keep it suspended until then.
  function ringContext() {
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

  // startRinging plays the ringtone every RING_PERIOD, while the browser lets
  // sound play. It returns the state of the ringing; stop ends it.
  function startRinging(onPlay) {
    var r = { timer: null, stopped: false, started: false };
    r.tick = function () {
      if (r.stopped) {
        return;
      }
      var ctx = ringContext();
      if (!ctx || ctx.state !== 'running') {
        return;
      }
      var now = Date.now();
      if (r.last && now - r.last < RING_PERIOD - 200) {
        return;
      }
      r.last = now;
      try {
        var master = ctx.createGain();
        master.gain.setValueAtTime(0.5, ctx.currentTime);
        master.connect(ctx.destination);
        purrRing(ctx, master, ctx.currentTime + 0.02);
        if (!r.started) {
          r.started = true;
          onPlay(true);
        }
      } catch (err) {
        // The ringtone is a courtesy; the dialog is what matters.
      }
    };
    r.tick();
    r.timer = setInterval(r.tick, 500);
    r.limit = setTimeout(function () {
      r.stop();
    }, RING_LIMIT);
    r.stop = function () {
      r.stopped = true;
      clearInterval(r.timer);
      clearTimeout(r.limit);
    };
    return r;
  }

  // openDialog opens the dialog of the call as a modal. When the person
  // dismisses it (Escape) it stays as a small card in the corner, so that the
  // call is not missed and the page is not blocked.
  function openDialog(dialog) {
    if (!dialog.__clowderCall) {
      dialog.__clowderCall = true;
      dialog.addEventListener('close', function () {
        if (dialog.isConnected && !dialog.open) {
          try {
            dialog.show();
          } catch (err) {
            // Already open.
          }
        }
      });
    }
    if (!dialog.open && dialog.showModal) {
      try {
        dialog.showModal();
      } catch (err) {
        dialog.setAttribute('open', '');
      }
    }
  }

  // The tabs elect the one that rings, as for the other events.
  function electCall(id, done) {
    var focused = document.hasFocus();
    var mine = { type: 'claim', id: id, tab: tabID, focused: focused, suppress: false, touched: touched };
    (claims[id] = claims[id] || []).push(mine);
    if (channel) {
      channel.postMessage(mine);
    }
    setTimeout(function () {
      var all = claims[id] || [];
      delete claims[id];
      all.sort(function (a, b) {
        if (a.touched !== b.touched) {
          return a.touched ? -1 : 1;
        }
        return a.tab < b.tab ? -1 : a.tab > b.tab ? 1 : 0;
      });
      if (all.length && all[0].tab === tabID) {
        done(all.some(function (c) { return c.focused; }));
      }
    }, channel ? ELECTION_WAIT : 0);
  }

  function stopCallRinging() {
    if (ringing) {
      ringing.stop();
      ringing.stopped = true;
      ringing = null;
    }
  }

  function checkCallDialog(slot) {
    var dialog = slot.querySelector('dialog.call-dialog');
    if (!dialog) {
      stopCallRinging();
      return;
    }
    var call = dialog.getAttribute('data-call');
    openDialog(dialog);
    if (ringing && ringing.call === call) {
      return;
    }
    stopCallRinging();
    var mark = { call: call, stop: function () {}, stopped: false };
    ringing = mark;
    var id = 'call-' + call;
    electCall(id, function (anyFocus) {
      if (ringing !== mark || !slot.querySelector('dialog.call-dialog')) {
        return;
      }
      var wantRing = dialog.hasAttribute('data-ring');
      var played = false;
      if (wantRing) {
        var r = startRinging(function (p) { played = p; });
        mark.stop = r.stop;
        mark.tick = r.tick;
        // The log gets the first sound, whenever the browser lets it play.
        var watch = setInterval(function () {
          if (r.started || r.stopped) {
            clearInterval(watch);
            log({ kind: 'call.ringing', sound: 'purr-ring', notified: notified, played: r.started });
          }
        }, 200);
      }
      var notified = false;
      if (dialog.hasAttribute('data-browser') && !anyFocus) {
        notified = show({ id: id, text: dialog.getAttribute('data-notice') || '', url: '' });
      }
      if (!wantRing) {
        log({ kind: 'call.ringing', sound: '', notified: notified, played: false });
      }
    });
  }

  var callSlot = document.getElementById('call-ringing');
  if (callSlot) {
    new MutationObserver(function () {
      checkCallDialog(callSlot);
    }).observe(callSlot, { childList: true, subtree: true });
    checkCallDialog(callSlot);
  }

  // Watch for what the server appends.
  new MutationObserver(function (records) {
    records.forEach(function (r) {
      r.addedNodes.forEach(function (node) {
        if (node.nodeType === 1 && node.classList.contains('notify-event')) {
          take(node);
        }
      });
    });
  }).observe(host, { childList: true });
})();
