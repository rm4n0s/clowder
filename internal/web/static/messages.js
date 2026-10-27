// The scrolling of a conversation (SPEC §7.11). No library and no build step
// (D13). The messages are in #message-pane, which scrolls: the newest at the
// bottom. The server (Datastar patches) changes the list; this script only
// keeps the person's place in it:
//
//   - the page opens at the bottom;
//   - a message that comes while the person is near the bottom, or that the
//     person has just sent, scrolls into view; one that comes while they read
//     older messages does not move the page;
//   - older messages that are put on top of the list (the sentinel at the top
//     loads them) leave what the person was reading where it is, by moving the
//     scroll position down by the height they added.
(function () {
  'use strict';

  var NEAR_BOTTOM = 120; // pixels from the bottom that still count as "at the bottom"

  var pane = document.getElementById('message-pane');
  if (!pane) {
    return;
  }

  var wasNearBottom = true;
  var forceBottom = false;
  var lastHeight = pane.scrollHeight;
  var lastFirst = firstMessage();

  function firstMessage() {
    var list = document.getElementById('message-list');
    return list ? list.firstElementChild : null;
  }

  function nearBottom() {
    return pane.scrollHeight - pane.scrollTop - pane.clientHeight <= NEAR_BOTTOM;
  }

  function toBottom() {
    pane.scrollTop = pane.scrollHeight;
  }

  // The person's own message always scrolls into view: the composer sets the
  // flag when its form is sent.
  document.addEventListener('submit', function (evt) {
    if (evt.target && evt.target.id === 'message-composer-form') {
      forceBottom = true;
    }
  });

  pane.addEventListener('scroll', function () {
    wasNearBottom = nearBottom();
    lastHeight = pane.scrollHeight;
  });

  new MutationObserver(function () {
    var height = pane.scrollHeight;
    var first = firstMessage();
    if (forceBottom || wasNearBottom) {
      toBottom();
      forceBottom = false;
    } else if (first !== lastFirst && lastFirst && first && first.compareDocumentPosition(lastFirst) & Node.DOCUMENT_POSITION_FOLLOWING) {
      // Messages were put before the one that was first: keep the place.
      pane.scrollTop += height - lastHeight;
    }
    lastFirst = firstMessage();
    lastHeight = pane.scrollHeight;
    wasNearBottom = nearBottom();
  }).observe(pane, { childList: true, subtree: true });

  // Images and the like change the height once they load.
  pane.addEventListener('load', function () {
    if (wasNearBottom) {
      toBottom();
    }
    lastHeight = pane.scrollHeight;
  }, true);

  toBottom();
  window.addEventListener('load', function () {
    if (wasNearBottom) {
      toBottom();
    }
  });
})();
