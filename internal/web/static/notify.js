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
