class Contact
  attr_reader :name, :fullname, :emails, :addresses, :phones

  def doctype
    "io.cozy.contacts"
  end

  def initialize(opts = {})
    first = opts[:given_name] || Faker::Name.first_name,
    last = opts[:family_name] || Faker::Name.last_name
    @name = { givenName: first, familyName: last }
    @fullname = "#{first} #{last}"

    email = opts[:email] || Faker::Internet.email([first, last, @fullname].sample)
    @emails = [{ address: email }]

    @addresses = [{
      street: opts[:street] || Faker::Address.street_name,
      city: opts[:city] || Faker::Address.city,
      post_code: opts[:post_code] || Faker::Address.postcode
    }]

    phone = opts[:phone] || Faker::PhoneNumber.cell_phone
    @phones = [{ number: phone }]
  end

  def as_json
    {
      name: @name,
      fullname: @fullname,
      email: @emails,
      address: @addresses,
      phone: @phones
    }
  end
end
