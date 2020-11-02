require_relative '../boot'
require 'minitest/autorun'
require 'faye/websocket'
require 'eventmachine'
require 'pry-rescue/minitest' unless ENV['CI']

describe "Export and import" do
  it "can be used to move data from a Cozy to another" do
    Helpers.scenario "export_import"
    Helpers.start_mailhog

    source = Instance.create name: "source"
    dest = Instance.create name: "dest"
    source.install_app "photos"

    # Create an album with some photos
    CozyFile.ensure_photos_in_cache
    folder = Folder.create source
    folder.couch_id.wont_be_empty
    album = Album.create source
    photos = CozyFile.create_photos source, dir_id: folder.couch_id
    photos.each { |p| album.add source, p }

    # Export the data from one Cozy and import them and the other
    sleep 1
    export = Export.new(source)
    export.run
    link = export.get_link
    import = Import.new(dest, link)
    import.precheck
    import.run

    # Wait that the import has finished via the websocket
    finished = false
    EM.run do
      ws = Faye::WebSocket::Client.new("ws://#{dest.domain}/move/importing/realtime")

      ws.on :message do |event|
        msg = JSON.parse(event.data)
        finished = msg.key? "redirect"
        ap msg unless finished
        ws.close
      end

      ws.on :close do
        EM.stop
      end

      # Add a timeout after 1 minute to avoid being stuck
      EM::Timer.new(60) do
        flunk "timeout"
        EM.stop
      end
    end
    assert finished
    import.get_mail

    dest.stack.reset_tokens

    # Check that everything has been moved
    moved = Album.find dest, album.couch_id
    assert_equal moved.name, album.name
    triggers = Trigger.all dest
    refute_nil(triggers.detect do |t|
      t.attributes.dig("message", "name") == "onPhotoUpload"
    end) # It's a service for the photos app

    # Check that the email from the destination was kept
    contacts = Contact.all dest
    me = contacts.detect(&:me)
    assert_equal me.emails[0]["address"], dest.email
    settings = Settings.instance dest
    assert_equal settings["email"], dest.email

    # It is the end
    assert_equal source.check, []
    assert_equal dest.check, []

    source.remove
    dest.remove
  end
end
