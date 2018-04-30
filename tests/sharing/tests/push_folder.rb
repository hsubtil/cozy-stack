#!/usr/bin/env ruby

require_relative '../boot'
require 'minitest/autorun'
require 'pry-rescue/minitest'

describe "A folder" do
  Helpers.scenario "push_folder"
  Helpers.start_mailhog

  it "can be shared to a recipient in push mode" do
    recipient_name = "Bob"

    # Create the instance
    inst = Instance.create name: "Alice"
    inst_recipient = Instance.create name: recipient_name

    # Create the folder
    folder = Folder.create inst
    folder.couch_id.wont_be_empty

    # Create the sharing
    contact = Contact.create inst, givenName: recipient_name
    sharing = Sharing.new
    sharing.rules << Rule.push(folder)
    sharing.members << inst << contact
    inst.register sharing

    # Accept the sharing
    sleep 1
    inst_recipient.accept sharing

    # Check the recipient's folder is the same as the sender's
    path = CGI.escape "/Partagés avec moi/#{folder.name}"
    folder_recipient = Folder.find_by_path inst_recipient, path
    assert_equal folder_recipient.name, folder.name

    # Check that the files are the same on disk
    sleep 7
    da = File.join Helpers.current_dir, inst.domain, folder.name
    db = File.join Helpers.current_dir, inst_recipient.domain,
                   Helpers::SHARED_WITH_ME, sharing.rules.first.title
    diff = Helpers.fsdiff da, db
    diff.must_be_empty
  end

end
