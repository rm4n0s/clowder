// Drag and drop of the cards of the board (SPEC §7.6). No library and no build
// step (D13): the HTML drag-and-drop events, on the document, so that the
// patches of Datastar, which replace the columns, never take a handler away.
//
// How it talks to the server: the script does not post anything itself. On a
// drop it works out the column and the place in it, writes them into the hidden
// form #board-drop (the fields "column" and "position", and the card in the
// path of its action) and submits that form. The form has Datastar's
// data-on:submit, so the move is a Datastar @post, and the server answers with
// a patch of #board-columns: the page always shows the server's order. Without
// Datastar the same form is an ordinary post that the server answers with a
// redirect to the board.
//
// A live update of the board must not move the card the person holds. While a
// card is held, #board has the attribute data-ignore-morph, which makes
// Datastar skip every patch inside it. The attribute goes when the card is
// dropped (before the move is sent, so that the answer is applied) or when the
// drag ends without a drop; then the script clicks the hidden link
// #board-refresh, which fetches the columns, so the patches that were skipped
// are not missed.
(function () {
  'use strict';

  var CARD = '.board-card[draggable="true"]';
  var held = null; // the card being dragged

  function boardEl() {
    return document.getElementById('board');
  }

  // cardsOf returns the cards of a list other than the one held, in order.
  function cardsOf(list) {
    return Array.prototype.filter.call(list.children, function (li) {
      return li !== held && li.classList.contains('board-card');
    });
  }

  // listAt returns the list of cards of the column an event happened in.
  function listAt(target) {
    var column = target && target.closest ? target.closest('.board-column') : null;
    return column ? column.querySelector('.board-cards') : null;
  }

  // placeIn returns where a card dropped at height y goes in the list: its
  // index among the other cards, and the card it goes before (null for the end).
  function placeIn(list, y) {
    var cards = cardsOf(list);
    var index = 0;
    for (var i = 0; i < cards.length; i++) {
      var box = cards[i].getBoundingClientRect();
      if (y > box.top + box.height / 2) {
        index = i + 1;
      }
    }
    return { index: index, before: cards[index] || null };
  }

  function clearMarks() {
    document.querySelectorAll('.drop-before, .drop-end, .drop-over').forEach(function (el) {
      el.classList.remove('drop-before', 'drop-end', 'drop-over');
    });
  }

  function mark(list, place) {
    clearMarks();
    list.classList.add('drop-over');
    if (place.before) {
      place.before.classList.add('drop-before');
    } else {
      list.classList.add('drop-end');
    }
  }

  // release lets Datastar patch the board again.
  function release() {
    var board = boardEl();
    if (board) {
      board.removeAttribute('data-ignore-morph');
    }
  }

  function finish() {
    if (held) {
      held.classList.remove('dragging');
    }
    clearMarks();
    held = null;
  }

  document.addEventListener('dragstart', function (e) {
    var card = e.target && e.target.closest ? e.target.closest(CARD) : null;
    if (!card) {
      return;
    }
    held = card;
    var board = boardEl();
    if (board) {
      board.setAttribute('data-ignore-morph', '');
    }
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move';
      // Firefox starts no drag without data.
      e.dataTransfer.setData('text/plain', card.getAttribute('data-task') || '');
    }
    // Later, so that the picture the browser takes of the card is the card.
    setTimeout(function () {
      if (held === card) {
        card.classList.add('dragging');
      }
    }, 0);
  });

  document.addEventListener('dragover', function (e) {
    if (!held) {
      return;
    }
    var list = listAt(e.target);
    if (!list) {
      return;
    }
    e.preventDefault(); // this is a place to drop
    if (e.dataTransfer) {
      e.dataTransfer.dropEffect = 'move';
    }
    mark(list, placeIn(list, e.clientY));
  });

  document.addEventListener('drop', function (e) {
    if (!held) {
      return;
    }
    var list = listAt(e.target);
    if (!list) {
      return;
    }
    e.preventDefault();
    var card = held;
    var place = placeIn(list, e.clientY);
    var column = list.getAttribute('data-column');
    var sameColumn = card.getAttribute('data-column') === column;
    // The place the card has now among the other cards of its column.
    var from = Array.prototype.filter.call(card.parentNode.children, function (li) {
      return li.classList.contains('board-card');
    }).indexOf(card);
    finish();
    if (sameColumn && place.index === from) {
      release(); // dropped where it was
      return;
    }
    var form = document.getElementById('board-drop');
    if (!form) {
      release();
      return;
    }
    var task = card.getAttribute('data-task');
    form.setAttribute('action', form.getAttribute('action').replace(/\/tasks\/\d+\/move$/, '/tasks/' + task + '/move'));
    form.elements.column.value = column;
    form.elements.position.value = String(place.index);
    release();
    form.requestSubmit();
  });

  document.addEventListener('dragend', function () {
    if (!held) {
      return; // not a card, or the drop has done everything
    }
    // The drag was cancelled: patches may have been skipped meanwhile.
    finish();
    release();
    var refresh = document.getElementById('board-refresh');
    if (refresh) {
      refresh.click();
    }
  });
})();
