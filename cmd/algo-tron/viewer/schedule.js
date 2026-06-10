// Optional schedule pane. Only active on deployments that pass a
// schedule-url (e.g. the GPN deployment). When the server template includes
// <section id="schedule-pane">, this fetches the schedule JSON on a minute
// timer and renders current + upcoming talks.
//
// Depends on: helpers.js (esc).

const scheduleEl = document.getElementById('schedule-pane');
if (scheduleEl) {
  const scheduleURL = scheduleEl.dataset.url;

  async function talks() {
    try {
      const response = await fetch(scheduleURL).then((r) => r.json());
      const now = new Date();
      const upcoming = response.talks.map((talk) => {
        const room = response.rooms.find((x) => x.id === talk.room) || {};
        return {
          title: typeof talk.title === 'string' ? talk.title : (talk.title.de || talk.title.en),
          room: typeof room.name === 'string' ? room.name : (room.name?.de || room.name?.en || 'Unknown'),
          start: new Date(talk.start),
          end: new Date(talk.end),
        };
      }).filter((talk) => talk.end > now).sort((a, b) => a.start - b.start);

      const current = upcoming.filter((talk) => talk.start <= now && talk.end > now);
      let next = upcoming.slice(current.length);
      if (next.length) next = next.filter((talk) => talk.start < new Date(+next[0].start + 7200000));
      currentTalks.innerHTML = current.map(talkHTML).join('');
      nextTalks.innerHTML = next.map(talkHTML).join('');
    } catch (e) {}
  }

  talks();
  setInterval(talks, 60000);
}

function talkHTML(talk) {
  return '<div><b>' + esc(talk.title) + '</b><br>' + esc(talk.room) + ' (' + talk.start.toTimeString().split(' ')[0] + ' - ' + talk.end.toTimeString().split(' ')[0] + ')</div>';
}
